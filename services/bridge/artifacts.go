package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Agent-produced deliverable publishing.
//
// A turn's output often IS the collaboration artifact: the spec the agent
// wrote, the code it changed, the image or report it generated. After each
// completed turn (and periodically during one) the bridge diffs the session's
// file manifest against its last-published snapshot and publishes every
// changed deliverable as a new artifact version, so the room's Docs panel and
// preview workbench reflect the agent's work without anyone copying files
// around.
//
// Known text files up to the inline cap publish as inline text versions (as
// before); every other file — images, PDFs, archives, large or non-UTF-8
// text — publishes as a blob version (multipart) with its MIME type, when the
// core reads files binary-safely (core.CapabilitySessionFileBytes). Each
// version carries the job and the prompting message (sourceJobId/sourceSeq)
// that produced it. A static web app (a directory holding index.html, build
// output included) publishes as one "site" snapshot instead (sites.go).
//
// The gateway routes artifact writes to bnbloc-room under the bridge's owner
// token; room membership authorizes exactly what the agent's owner can see.

// maxPublishFiles bounds one pass's publish fan-out. A turn that rewrote the
// world publishes 24 files, not a 200-file dump: the Docs panel is a review
// surface, not a mirror. Files the turn itself wrote go first.
const maxPublishFiles = 24

// dependencyDirs are workspace directories that hold dependencies,
// environments or generated reports: never a deliverable, never a site, never
// site content. Any path segment starting with "." (.git, .venv, .room,
// caches, dotfiles such as .env) is treated the same way.
var dependencyDirs = map[string]bool{
	"node_modules": true, "bower_components": true, "vendor": true,
	"venv": true, "__pycache__": true, "target": true, "coverage": true,
}

// buildOutputDirs hold web build output. Their files are never individual
// deliverables, but a site root lives there (sites.go): a built static web
// app publishes as one site snapshot.
var buildOutputDirs = map[string]bool{"dist": true, "build": true, "out": true}

// dependencyPath reports a path with a hidden or dependency-dir segment
// (any segment for dirs; the last segment of a file path only when hidden).
func dependencyPath(rel string, isDir bool) bool {
	segs := strings.Split(rel, "/")
	for i, seg := range segs {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return true
		}
		if (isDir || i < len(segs)-1) && dependencyDirs[seg] {
			return true
		}
	}
	return false
}

// ignoredDeliverableExts are intermediate build/editor files: never a
// deliverable, and numerous enough to crowd real ones out of a pass.
var ignoredDeliverableExts = map[string]bool{
	".o": true, ".obj": true, ".pyc": true, ".class": true, ".swp": true, ".tmp": true,
}

// ignoredDeliverable reports a workspace path the publisher never publishes.
func ignoredDeliverable(rel string) bool {
	if ignoredDeliverableExts[strings.ToLower(path.Ext(rel))] || dependencyPath(rel, false) || corepkg.ProtectedWorkspacePath(rel) {
		return true
	}
	segs := strings.Split(rel, "/")
	for _, seg := range segs[:len(segs)-1] {
		if buildOutputDirs[seg] {
			return true
		}
	}
	return false
}

// artifactKindForContent types a text deliverable from its name and content.
// An .html file that carries a script is a PROGRAM — the author intended it
// to run (interactive artifact), so it publishes as kind "app" and members get
// the live sandboxed runner instead of source text. A scriptless .html is a
// document to read and stays kind "html". Extension alone cannot tell these
// apart.
func artifactKindForContent(name, content string) string {
	kind := store.ArtifactKindForFile(name, "")
	if kind == store.ArtifactKindHTML && strings.Contains(strings.ToLower(content), "<script") {
		return store.ArtifactKindApp
	}
	return kind
}

// fileStamp captures the (size, mtime) pair a change detection compares. Size
// alone misses a same-length rewrite, which is common for code edits.
type fileStamp struct {
	Bytes   int64
	MtimeMs int64
}

// deliverableTurn scopes one publish pass: when the turn began (a cold-start
// session publishes only what this turn wrote) and the job + prompting
// message the published versions link back to.
type deliverableTurn struct {
	StartMs   int64
	JobID     string
	SourceSeq int64
}

// sessionFiles resolves the session core's optional SessionFiles capability:
// the core-native listing/reading of the agent's workspace files, and whether
// its reads are binary-safe. A core that does not offer it (a Core Protocol
// daemon not advertising sessionFiles, a manifest without the session-file
// ops) publishes no deliverables.
func (b *Bridge) sessionFiles(ctx context.Context, coreName string) (files corepkg.SessionFiles, ep corepkg.Endpoint, binarySafe, ok bool) {
	entry, found := b.coreEntry(coreName)
	if !found || !entry.endpoint.OK() {
		return nil, corepkg.Endpoint{}, false, false
	}
	files, ok = corepkg.As[corepkg.SessionFiles](ctx, entry.provider, entry.endpoint, corepkg.CapabilitySessionFiles)
	if !ok {
		return nil, corepkg.Endpoint{}, false, false
	}
	_, binarySafe = corepkg.As[corepkg.SessionFiles](ctx, entry.provider, entry.endpoint, corepkg.CapabilitySessionFileBytes)
	return files, entry.endpoint, binarySafe, true
}

// publishDeliverables publishes the turn's produced files as artifact
// versions. rs.lastPublished holds the file-listing snapshot after the previous
// publish; the diff between it and the current listing is what this turn
// produced. turn.StartMs scopes a cold start to what the current turn actually
// wrote: files older than the turn are the session's history, not this turn's
// deliverables, and a member joining the room must not have that history dumped
// into Docs. On a warm start the snapshot diff drives detection instead. Never
// fails the turn: publishing is presentation, not correctness.
func (b *Bridge) publishDeliverables(ctx context.Context, room, channelID string, rs *roomSession, turn deliverableTurn) {
	if rs == nil || rs.workspaceID == "" || rs.sessionID == "" {
		slog.Debug("bnbloc bridge deliverables skipped: no session", "event", "bnbloc.bridge.deliverables_skip")
		return
	}
	// A turn's publish runs on its own goroutine (publishTurn), so it can
	// overlap the next turn's; both read and replace lastPublished:
	// serialize them per session.
	rs.publishMu.Lock()
	defer rs.publishMu.Unlock()
	files, ep, binarySafe, ok := b.sessionFiles(ctx, rs.core)
	if !ok {
		slog.Debug("bnbloc bridge deliverables skipped: core offers no session files", "event", "bnbloc.bridge.deliverables_skip", "core", rs.core)
		return
	}
	current, err := files.ListSessionFiles(ctx, ep, rs.workspaceID, rs.sessionID)
	if err != nil {
		slog.Debug("bnbloc bridge deliverables skipped: list session files failed", "event", "bnbloc.bridge.deliverables_skip", "core", rs.core, "ws", rs.workspaceID, "error", err)
		return
	}
	if len(current) == 0 {
		slog.Debug("bnbloc bridge deliverables skipped: no session files", "event", "bnbloc.bridge.deliverables_skip", "ws", rs.workspaceID)
		return
	}
	now := fileStamps(current)
	cold := rs.lastPublished == nil

	// Changed = written during this turn (mtime), new since the last publish,
	// or a (size, mtime) drift. The mtime arm is what a cold start uses: it
	// publishes the files THIS turn produced while leaving the session's older
	// history unpublished.
	const skewMs = 2000 // manifest mtime vs turn-start clock skew headroom
	touched := func(f corepkg.SessionFile) bool { return f.MtimeMs >= turn.StartMs-skewMs }
	fileChanged := func(f corepkg.SessionFile) bool {
		if cold {
			// Pre-turn history on a cold start is not this turn's work.
			return touched(f)
		}
		// Warm: a stamp drift since the previous turn's publish.
		prev, existed := rs.lastPublished[f.Path]
		return !existed || prev != (fileStamp{Bytes: f.Bytes, MtimeMs: f.MtimeMs})
	}
	// changed are the individual deliverables; changedPaths every changed or
	// removed path, build output included, which site detection diffs.
	var changed []corepkg.SessionFile
	var changedPaths []string
	for _, f := range current {
		if !fileChanged(f) {
			continue
		}
		changedPaths = append(changedPaths, f.Path)
		if !ignoredDeliverable(f.Path) {
			changed = append(changed, f)
		}
	}
	for p := range rs.lastPublished {
		if _, still := now[p]; !still {
			changedPaths = append(changedPaths, p)
		}
	}
	if len(changedPaths) == 0 {
		rs.lastPublished = now
		return
	}
	// The room's blob cap decides which files can publish at all. Unknown
	// (gateway unreachable) means nothing can publish: keep the snapshot so
	// the next pass retries these changes.
	maxBytes, err := b.artifactMaxBytes(ctx, room)
	if err != nil {
		slog.Debug("bnbloc bridge deliverables deferred: artifact limits unavailable",
			"event", "bnbloc.bridge.deliverables_deferred", "room", room, "error", err)
		return
	}
	rs.lastPublished = now
	// Static web apps publish as one site snapshot each; their web assets
	// are then not individual deliverables. Sites need exact bytes.
	if binarySafe {
		sites := b.publishSites(ctx, room, channelID, rs, files, ep, current, changedPaths, maxBytes, turn)
		changed = slices.DeleteFunc(changed, func(f corepkg.SessionFile) bool { return sites.folds(f.Path) })
	}
	// Files this turn wrote first, then biggest, then alphabetical: the
	// substantial deliverables of the current work surface before older
	// drift and generated trivia.
	sort.Slice(changed, func(i, j int) bool {
		if ti, tj := touched(changed[i]), touched(changed[j]); ti != tj {
			return ti
		}
		if changed[i].Bytes != changed[j].Bytes {
			return changed[i].Bytes > changed[j].Bytes
		}
		return changed[i].Path < changed[j].Path
	})
	published := 0
	for _, f := range changed {
		if published >= maxPublishFiles {
			break
		}
		if f.Bytes > maxBytes {
			continue // over the room's cap: the room would refuse it
		}
		_, isText := store.ArtifactTextMIME(f.Path)
		if !isText && !binarySafe {
			continue // the core cannot read binary files faithfully
		}
		content, err := files.ReadSessionFile(ctx, ep, rs.workspaceID, rs.sessionID, f.Path)
		if err != nil {
			slog.Debug("bnbloc bridge deliverable read failed",
				"event", "bnbloc.bridge.deliverable_read_failed", "room", room, "file", f.Path, "error", err)
			continue
		}
		d := deliverable{rel: f.Path, content: content, turn: turn}
		switch {
		case isText && len(content) <= store.ArtifactInlineMaxBytes && utf8.Valid(content):
			d.inline = true
			d.kind = artifactKindForContent(f.Path, string(content))
		case !binarySafe:
			continue // not inline-publishable text, and the bytes are not exact
		case int64(len(content)) > maxBytes:
			continue
		default:
			d.mimeType = store.ArtifactMIME(f.Path, sniffHead(content))
			d.kind = store.ArtifactKindForFile(f.Path, d.mimeType)
		}
		published++
		if err := b.publishArtifactNow(ctx, room, channelID, rs.workspaceID, rs.sessionID, d, "agent"); err != nil {
			slog.Debug("bnbloc bridge artifact publish failed",
				"event", "bnbloc.bridge.artifact_publish_failed", "room", room, "file", f.Path, "error", err)
		}
	}
}

func sniffHead(b []byte) []byte {
	if len(b) > 512 {
		return b[:512]
	}
	return b
}

func fileStamps(files []corepkg.SessionFile) map[string]fileStamp {
	m := make(map[string]fileStamp, len(files))
	for _, f := range files {
		m[f.Path] = fileStamp{Bytes: f.Bytes, MtimeMs: f.MtimeMs}
	}
	return m
}

// artifactMaxBytes asks the room for its blob version cap (the artifact
// list's maxBytes): the room's deployment owns that value.
func (b *Bridge) artifactMaxBytes(ctx context.Context, room string) (int64, error) {
	raw, err := b.gatewayGET(ctx, "/v1/rooms/"+url.PathEscape(room)+"/artifacts?limit=1")
	if err != nil {
		return 0, err
	}
	var resp struct {
		MaxBytes int64 `json:"maxBytes"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.MaxBytes <= 0 {
		return 0, fmt.Errorf("artifact list carries no maxBytes (%v)", err)
	}
	return resp.MaxBytes, nil
}

// artifactIDForFile derives the stable artifact id for a session file, so the
// same file's turns publish versions of ONE artifact instead of duplicates.
func artifactIDForFile(workspaceID, sessionID, rel string) string {
	return "artf_" + shortHash(workspaceID+"/"+sessionID+"/"+rel)
}

// deliverable is one file to publish: inline text, or blob bytes with a MIME
// type. rel derives the stable artifact id; a site's rel is its root with a
// trailing slash, which no file path has. title and fileName default to rel
// and its base name.
type deliverable struct {
	rel      string
	kind     string
	title    string
	fileName string
	mimeType string // blob versions
	inline   bool
	content  []byte
	turn     deliverableTurn
}

// publishArtifactNow is the real entry: create-or-attach then CAS publish.
func (b *Bridge) publishArtifactNow(ctx context.Context, room, channelID, workspaceID, sessionID string, d deliverable, author string) error {
	id := artifactIDForFile(workspaceID, sessionID, d.rel)
	roomPath := "/v1/rooms/" + url.PathEscape(room) + "/artifacts"
	title, fileName := d.title, d.fileName
	if title == "" {
		title = d.rel
	}
	if fileName == "" {
		fileName = path.Base(d.rel)
	}
	// createArtifactBody / publishVersionBody mirror the room service shapes.
	create := map[string]any{
		"artifactId": id, "kind": d.kind, "title": title, "channelId": channelID,
		"language": languageFor(d.rel), "fileName": fileName,
	}
	if _, err := b.gatewayPOST(ctx, roomPath, create); err != nil {
		return fmt.Errorf("create artifact: %w", err)
	}
	// Read the current version, then publish base+1.
	base := 0
	if raw, err := b.gatewayGET(ctx, roomPath+"/"+id); err == nil {
		var cur struct {
			Artifact struct {
				CurrentVersion int `json:"currentVersion"`
			} `json:"artifact"`
		}
		if json.Unmarshal(raw, &cur) == nil {
			base = cur.Artifact.CurrentVersion
		}
	}
	summary := "published by " + author
	versionsPath := roomPath + "/" + id + "/versions"
	if d.inline {
		body := map[string]any{
			"baseVersion": base, "content": string(d.content), "summary": summary,
			"sourceJobId": d.turn.JobID, "sourceSeq": d.turn.SourceSeq,
		}
		_, err := b.gatewayPOST(ctx, versionsPath, body)
		return err
	}
	fields := map[string]string{
		"baseVersion": strconv.Itoa(base), "summary": summary,
		"sourceJobId": d.turn.JobID, "sourceSeq": strconv.FormatInt(d.turn.SourceSeq, 10),
	}
	return b.gatewayPOSTFile(ctx, versionsPath, fields, fileName, d.mimeType, d.content)
}

// artifactUploadTimeout bounds one blob version upload. The bridge's shared
// HTTP client has a short overall timeout sized for JSON calls; a file of up
// to the room's cap needs longer on a slow link.
const artifactUploadTimeout = 10 * time.Minute

// gatewayPOSTFile posts a multipart body (form fields + one "file" part) to
// the gateway under the owner token.
func (b *Bridge) gatewayPOSTFile(ctx context.Context, p string, fields map[string]string, fileName, contentType string, content []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := mw.WriteField(k, fields[k]); err != nil {
			return err
		}
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": fileName}))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, artifactUploadTimeout)
	defer cancel()
	client := &http.Client{Transport: b.http.Transport}
	_, err = b.doRaw(ctx, client, http.MethodPost, b.cfg.GatewayBaseURL+p, b.gatewayAuth(ctx), mw.FormDataContentType(), &buf)
	return err
}

func languageFor(rel string) string {
	ext := strings.ToLower(path.Ext(rel))
	return strings.TrimPrefix(ext, ".")
}

// shortHash derives a short stable digest for id derivation.
func shortHash(s string) string {
	sum := store.HashArtifactContent(s)
	return sum[:24]
}
