package bridge

import (
	"context"
	"errors"
	"html"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// Site snapshots (PRD 2026-10-09 "Bridge (site snapshots)").
//
// An agent that builds a static web app — plain HTML/CSS/JS, or a build
// output such as dist/ — has produced ONE deliverable, not a pile of assets.
// Each publish pass the bridge finds the site roots among the session files,
// packs every changed root into a deterministic tarbundle and publishes it as
// a blob version of kind "site", which the gateway serves into a sandboxed
// frame. Packing is deterministic, so an unchanged site re-publishes as the
// room's "unchanged" no-op. The roots' web assets are then left out of the
// individual deliverables; their other files (README.md, …) still publish.

// maxSitesPerPass bounds one pass's site snapshots; further changed roots
// are deferred to the next pass.
const maxSitesPerPass = 3

// siteRootIndex is the document that makes a directory a site root.
const siteRootIndex = "index.html"

// buildSourceExts are module types a browser cannot load without a build
// step: an index.html whose script src names one is a project's source
// root, and its build output is the site.
var buildSourceExts = map[string]bool{".ts": true, ".tsx": true, ".jsx": true, ".vue": true, ".svelte": true}

// siteAssetExts are the web-asset files a site snapshot folds: under an
// accepted root they are not published individually.
var siteAssetExts = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".js": true, ".mjs": true,
	".wasm": true, ".json": true, ".map": true, ".webmanifest": true, ".ico": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".avif": true, ".svg": true, ".bmp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp3": true, ".mp4": true, ".webm": true, ".ogg": true, ".wav": true, ".m4a": true,
}

var (
	scriptSrcRE = regexp.MustCompile(`(?is)<script\b[^>]*?\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	titleRE     = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
)

var errSiteOverRoomCap = errors.New("site archive exceeds the room's artifact cap")

// maxSiteTitleRunes keeps a site title within the room's title cap.
const maxSiteTitleRunes = 120

// siteRoots is the outcome of one pass's site detection: the accepted roots
// (published, deferred or over the caps alike), whose web assets are folded.
type siteRoots []string

// folds reports a session file that a site snapshot carries instead of an
// individual deliverable.
func (s siteRoots) folds(rel string) bool {
	if !siteAssetExts[strings.ToLower(path.Ext(rel))] {
		return false
	}
	for _, root := range s {
		if _, ok := siteContentPath(root, rel); ok {
			return true
		}
	}
	return false
}

// siteContentPath maps a session file to its path inside root's bundle; ok
// is false outside the root or under a dependency/hidden dir.
func siteContentPath(root, rel string) (string, bool) {
	inner := rel
	if root != "" {
		var found bool
		if inner, found = strings.CutPrefix(rel, root+"/"); !found {
			return "", false
		}
	}
	if inner == "" || dependencyPath(inner, false) {
		return "", false
	}
	return inner, true
}

// siteDir is the directory of a session file ("" for the workspace root).
func siteDir(rel string) string {
	if d := path.Dir(rel); d != "." {
		return d
	}
	return ""
}

// siteRootCandidates lists the directories holding an index.html that are
// not inside a dependency or hidden dir, shallowest first (an outer root
// claims the roots nested in it).
func siteRootCandidates(files []corepkg.SessionFile) []string {
	var roots []string
	for _, f := range files {
		if path.Base(f.Path) != siteRootIndex {
			continue
		}
		dir := siteDir(f.Path)
		if dir != "" && dependencyPath(dir, true) {
			continue
		}
		roots = append(roots, dir)
	}
	sort.Slice(roots, func(i, j int) bool {
		di, dj := siteDepth(roots[i]), siteDepth(roots[j])
		if di != dj {
			return di < dj
		}
		return roots[i] < roots[j]
	})
	return roots
}

func siteDepth(root string) int {
	if root == "" {
		return 0
	}
	return strings.Count(root, "/") + 1
}

// siteNeedsBuild reports an index.html that loads a module a browser cannot
// run without a build step.
func siteNeedsBuild(indexHTML string) bool {
	for _, m := range scriptSrcRE.FindAllStringSubmatch(indexHTML, -1) {
		src := m[1] + m[2] + m[3]
		if i := strings.IndexAny(src, "?#"); i >= 0 {
			src = src[:i]
		}
		if buildSourceExts[strings.ToLower(path.Ext(src))] {
			return true
		}
	}
	return false
}

// siteTitle is the index.html <title>, else the root dir name ("site" for
// the workspace root).
func siteTitle(root, indexHTML string) string {
	if m := titleRE.FindStringSubmatch(indexHTML); m != nil {
		if t := strings.Join(strings.Fields(html.UnescapeString(m[1])), " "); t != "" {
			if r := []rune(t); len(r) > maxSiteTitleRunes {
				t = string(r[:maxSiteTitleRunes])
			}
			return t
		}
	}
	return siteFileName(root)
}

// siteFileName is the root dir name, or "site" for the workspace root.
func siteFileName(root string) string {
	if root == "" {
		return "site"
	}
	return path.Base(root)
}

// acceptedSite is a root this pass publishes.
type acceptedSite struct {
	root  string
	title string
	index []byte // index.html as read for detection
	// lastMtime orders sites for the per-pass cap: the most recently
	// written first.
	lastMtime int64
}

// publishSites detects this pass's changed site roots (changedPaths holds
// every changed or removed path, build output included) and publishes up to
// maxSitesPerPass of them. It returns every accepted root so their web
// assets are folded out of the individual deliverables. A deferred root's
// index.html stamp is reset so the next pass sees the root as changed.
func (b *Bridge) publishSites(ctx context.Context, room, channelID string, rs *roomSession, files corepkg.SessionFiles, ep corepkg.Endpoint, current []corepkg.SessionFile, changedPaths []string, maxBytes int64, turn deliverableTurn) siteRoots {
	var accepted []acceptedSite
	var roots siteRoots
	for _, root := range siteRootCandidates(current) {
		if roots.claims(root) {
			continue // nested in an accepted root: part of the outer site
		}
		if !siteRootChanged(root, changedPaths) {
			continue
		}
		indexPath := path.Join(root, siteRootIndex)
		raw, err := files.ReadSessionFile(ctx, ep, rs.workspaceID, rs.sessionID, indexPath)
		if err != nil {
			slog.Info("bnbloc bridge site skipped: index read failed",
				"event", "bnbloc.bridge.site_skip", "room", room, "root", indexPath, "error", err)
			continue
		}
		if siteNeedsBuild(string(raw)) {
			continue // a source root: its build output is the site
		}
		roots = append(roots, root)
		accepted = append(accepted, acceptedSite{root: root, title: siteTitle(root, string(raw)), index: raw, lastMtime: siteLastMtime(root, current)})
	}
	sort.SliceStable(accepted, func(i, j int) bool { return accepted[i].lastMtime > accepted[j].lastMtime })
	for i, site := range accepted {
		if i >= maxSitesPerPass {
			slog.Info("bnbloc bridge site deferred: per-pass cap",
				"event", "bnbloc.bridge.site_deferred", "room", room, "root", site.root, "cap", maxSitesPerPass)
			delete(rs.lastPublished, path.Join(site.root, siteRootIndex))
			continue
		}
		b.publishSite(ctx, room, channelID, rs, files, ep, current, site, maxBytes, turn)
	}
	return roots
}

// claims reports a root nested in (or equal to) an accepted root.
func (s siteRoots) claims(root string) bool {
	for _, outer := range s {
		if outer == "" || root == outer || strings.HasPrefix(root, outer+"/") {
			return true
		}
	}
	return false
}

// siteRootChanged reports a changed or removed file inside root's content.
func siteRootChanged(root string, changedPaths []string) bool {
	for _, p := range changedPaths {
		if _, ok := siteContentPath(root, p); ok {
			return true
		}
	}
	return false
}

func siteLastMtime(root string, current []corepkg.SessionFile) int64 {
	var last int64
	for _, f := range current {
		if _, ok := siteContentPath(root, f.Path); ok && f.MtimeMs > last {
			last = f.MtimeMs
		}
	}
	return last
}

// publishSite reads, packs and publishes one site root. Paths the bundle
// rules refuse are skipped (logged); a root over tarbundle.SiteLimits or the
// room's artifact cap, or one whose file vanished mid-read, is not published
// this pass.
func (b *Bridge) publishSite(ctx context.Context, room, channelID string, rs *roomSession, files corepkg.SessionFiles, ep corepkg.Endpoint, current []corepkg.SessionFile, site acceptedSite, maxBytes int64, turn deliverableTurn) {
	limit := min(int64(tarbundle.SiteLimits.MaxBytes), maxBytes)
	var content []corepkg.SessionFile
	var inner []string
	var invalid []string
	var total int64
	for _, f := range current {
		p, ok := siteContentPath(site.root, f.Path)
		if !ok {
			continue
		}
		if !pkgmeta.ValidBundlePath(p) {
			invalid = append(invalid, p)
			continue
		}
		content = append(content, f)
		inner = append(inner, p)
		total += f.Bytes
	}
	if len(invalid) > 0 {
		slog.Info("bnbloc bridge site files skipped: not valid bundle paths",
			"event", "bnbloc.bridge.site_paths_skipped", "room", room, "root", site.root,
			"count", len(invalid), "examples", invalid[:min(len(invalid), 5)])
	}
	if len(content) > tarbundle.SiteLimits.MaxFiles || total > limit {
		slog.Info("bnbloc bridge site not published: over the size caps",
			"event", "bnbloc.bridge.site_too_large", "room", room, "root", site.root,
			"files", len(content), "bytes", total, "maxFiles", tarbundle.SiteLimits.MaxFiles, "maxBytes", limit)
		return
	}
	bundle := make(map[string][]byte, len(content))
	for i, f := range content {
		if inner[i] == siteRootIndex {
			bundle[inner[i]] = site.index
			continue
		}
		data, err := files.ReadSessionFile(ctx, ep, rs.workspaceID, rs.sessionID, f.Path)
		if err != nil {
			slog.Info("bnbloc bridge site not published: file read failed",
				"event", "bnbloc.bridge.site_skip", "room", room, "root", site.root, "file", f.Path, "error", err)
			return
		}
		bundle[inner[i]] = data
	}
	tarball, err := tarbundle.PackFiles(bundle, tarbundle.SiteLimits)
	if err == nil && int64(len(tarball)) > maxBytes {
		err = errSiteOverRoomCap
	}
	if err != nil {
		slog.Info("bnbloc bridge site not published: pack failed",
			"event", "bnbloc.bridge.site_too_large", "room", room, "root", site.root, "error", err)
		return
	}
	d := deliverable{
		rel: site.root + "/", kind: store.ArtifactKindSite, title: site.title, fileName: siteFileName(site.root),
		mimeType: tarbundle.SiteMIME, content: tarball, turn: turn,
	}
	if err := b.publishArtifactNow(ctx, room, channelID, rs.workspaceID, rs.sessionID, d, "agent"); err != nil {
		slog.Debug("bnbloc bridge site publish failed",
			"event", "bnbloc.bridge.artifact_publish_failed", "room", room, "root", site.root, "error", err)
	}
}
