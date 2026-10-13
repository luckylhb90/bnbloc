package contentpkg

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Works (docs/prd/2026-10-13-app-gallery.md §3,
// docs/prd/2026-10-13-works-gallery-and-make-similar.md §2): a snapshot of
// one deliverable version published as a template, of any type. A site or
// an app is its folder as published (index.html at the root, or a build's
// dist/), within a site snapshot's caps; any other deliverable is its one
// file at the bundle's root, which the manifest names (file). Beside it a
// work may carry a card cover (tarbundle.WorkCover) and a README.md for the
// detail page. A work holds no data: an app's data lives in its site's
// data space, which is never part of a bundle. Nothing in it runs on a
// member's machine (a site's code runs only in the sandboxed site frame),
// so its scripts need no permission and a work requires nothing.

// Work files.
const (
	// SiteEntry is the document that makes a folder a site.
	SiteEntry = "index.html"
	// siteDistDir is a build's output folder, the other entry a site may
	// have (dist/index.html).
	siteDistDir = "dist"
	// workRenamedPrefix prefixes a deliverable file whose name is one the
	// bundle reserves (component.json, README.md).
	workRenamedPrefix = "work-"
)

// WorkLimits are a work bundle's caps: a site snapshot's (a single file is
// held to the same bytes) plus its manifest.
var WorkLimits = tarbundle.Limits{MaxBytes: tarbundle.SiteLimits.MaxBytes + maxWorkManifest, MaxFiles: tarbundle.SiteLimits.MaxFiles + 1}

// maxWorkManifest is the room a work's component.json takes beyond the
// site's own caps.
const maxWorkManifest = 1 << 20

func init() {
	register(KindSpec{Kind: KindWork, Short: "work", Limits: WorkLimits, Snapshot: true,
		validate: validateWork, fromForm: workFromForm})
}

// WorkDeliverable is what a work's type is derived from: the deliverable
// as members see it (its kind, and whether it is an app: the projection's
// isApp, store.Artifact.IsApp) and for anything but a site the file as the
// work holds it (its name and first bytes).
type WorkDeliverable struct {
	Artifact wire.Artifact
	FileName string
	Head     []byte
}

// WorkTypeOf is the one rule of a work's type (wire.WorkType): a site is
// an app when it is one (store.Artifact.IsApp), else a site; an HTML
// document is a site (it runs in the sandboxed site frame); any other file
// is typed by its media type (store.ArtifactMIME: its name, else its
// bytes).
func WorkTypeOf(d WorkDeliverable) string {
	switch d.Artifact.Kind {
	case store.ArtifactKindSite:
		if d.Artifact.IsApp {
			return wire.WorkTypeApp
		}
		return wire.WorkTypeSite
	case store.ArtifactKindHTML, store.ArtifactKindApp:
		return wire.WorkTypeSite
	}
	return workTypeOfMIME(store.ArtifactMIME(d.FileName, d.Head))
}

// workMIMETypes types the documents, slides and tables by media type
// (images, video and audio go by their top-level type).
var workMIMETypes = map[string]string{
	"text/markdown":   wire.WorkTypeDocument,
	"text/plain":      wire.WorkTypeDocument,
	"application/pdf": wire.WorkTypeDocument,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": wire.WorkTypeDocument,
	"application/msword":                      wire.WorkTypeDocument,
	"application/rtf":                         wire.WorkTypeDocument,
	"application/vnd.oasis.opendocument.text": wire.WorkTypeDocument,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": wire.WorkTypeSlides,
	"application/vnd.ms-powerpoint":                                             wire.WorkTypeSlides,
	"application/vnd.oasis.opendocument.presentation":                           wire.WorkTypeSlides,
	"text/csv":                  wire.WorkTypeTable,
	"text/tab-separated-values": wire.WorkTypeTable,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": wire.WorkTypeTable,
	"application/vnd.ms-excel":                       wire.WorkTypeTable,
	"application/vnd.oasis.opendocument.spreadsheet": wire.WorkTypeTable,
	"text/html": wire.WorkTypeSite,
}

func workTypeOfMIME(mimeType string) string {
	mt := store.MediaType(mimeType)
	if t, ok := workMIMETypes[mt]; ok {
		return t
	}
	switch top, _, _ := strings.Cut(mt, "/"); top {
	case "image":
		return wire.WorkTypeImage
	case "video":
		return wire.WorkTypeVideo
	case "audio":
		return wire.WorkTypeAudio
	}
	return wire.WorkTypeOther
}

// workHead is the start of a file the type rule sniffs.
func workHead(data []byte) []byte { return data[:min(len(data), 512)] }

// validateWork: a known type that matches the work's files (a site's entry
// point, or the one file the manifest names, typed by WorkTypeOf), no
// protected workspace path, a cover (when it ships one) that
// tarbundle.SiteCover accepts (an image of the type its name says, within
// wire.MaxWorkCoverBytes; an SVG is never one), a README.md that is text, a
// category from wire.WorkCategories, and none of the other kinds' fields
// (a work requires nothing).
func validateWork(p *Package) error {
	m := p.Manifest
	switch {
	case len(m.QuickPrompts) > 0 || m.Welcome != "":
		return errors.New("quick prompts and a welcome belong to experts")
	case len(m.Skills) > 0:
		return errors.New("a work lists no skills")
	case len(m.Prompts) > 0:
		return errors.New("prompts belong to prompt packs")
	case len(m.Requires.Skills)+len(m.Requires.Prompts)+len(m.Requires.MCP)+len(m.Requires.Permissions) > 0:
		return errors.New("a work requires nothing: it never runs on a member's machine")
	}
	if m.Category != "" && !wire.ValidWorkCategory(m.Category) {
		return fmt.Errorf("category %q: a work's category is one of %s", m.Category, strings.Join(wire.WorkCategories, ", "))
	}
	for _, f := range p.Bundle.Paths() {
		if core.ProtectedWorkspacePath(f) {
			return fmt.Errorf("%s: a work holds no agent configuration or room files", f)
		}
	}
	if err := validateWorkFiles(p); err != nil {
		return err
	}
	if _, _, err := WorkCover(p); err != nil {
		return err
	}
	if _, ok := p.Bundle.File(ReadmeFile); ok {
		if _, err := readText(p.Bundle, ReadmeFile, maxInstructions); err != nil {
			return err
		}
	}
	return nil
}

// validateWorkFiles holds the files to the type: a site or an app is a
// folder with an entry point (and names no file); any other type is the
// one file the manifest names, at the root, of that type, beside only the
// manifest, a README.md and a cover.
func validateWorkFiles(p *Package) error {
	m := p.Manifest
	if !validWorkType(m.Type) {
		return fmt.Errorf("type %q: a work's type is one of %s", m.Type, strings.Join(wire.WorkTypes, ", "))
	}
	if wire.WorkTypeIsSite(m.Type) {
		if m.File != "" {
			return fmt.Errorf("a %s work is a folder: it names no file", m.Type)
		}
		if _, ok := WorkEntryOf(p.Bundle); !ok {
			return fmt.Errorf("a %s work needs %s at its root (or %s/%s)", m.Type, SiteEntry, siteDistDir, SiteEntry)
		}
		return nil
	}
	if m.File == "" || strings.Contains(m.File, "/") || m.File == ManifestFile {
		return fmt.Errorf("a %s work names its file at the bundle's root (file)", m.Type)
	}
	data, ok := p.Bundle.File(m.File)
	if !ok {
		return fmt.Errorf("the work's file %s is not in the bundle", m.File)
	}
	if t := WorkTypeOf(WorkDeliverable{FileName: m.File, Head: workHead(data)}); t != m.Type {
		return fmt.Errorf("%s is a %s, not a %s", m.File, t, m.Type)
	}
	cover, _, _ := tarbundle.SiteCover(p.Bundle)
	for _, f := range p.Bundle.Paths() {
		switch f {
		case ManifestFile, ReadmeFile, m.File, cover.File:
		default:
			return fmt.Errorf("%s: a %s work holds only its file, %s, %s and a cover", f, m.Type, ManifestFile, ReadmeFile)
		}
	}
	return nil
}

func validWorkType(t string) bool {
	for _, k := range wire.WorkTypes {
		if t == k {
			return true
		}
	}
	return false
}

// WorkCover is a work's cover (tarbundle.WorkCover, the cover order): its
// cover file, else an image work's image.
func WorkCover(p *Package) (tarbundle.Cover, bool, error) {
	image := ""
	if p.Manifest.Type == wire.WorkTypeImage {
		image = p.Manifest.File
	}
	return tarbundle.WorkCover(p.Bundle, image)
}

// WorkEntryOf is the entry point of a site folder: index.html at the root,
// else dist/index.html.
func WorkEntryOf(b *tarbundle.Bundle) (string, bool) {
	for _, e := range []string{SiteEntry, siteDistDir + "/" + SiteEntry} {
		if _, ok := b.File(e); ok {
			return e, true
		}
	}
	return "", false
}

// WorkSite is the site a validated app or site work copies into a room
// ("Use this app") and runs as its demo: its files without the manifest, a
// dist/ entry's files at the root (with the root's cover and README.md
// beside them).
func WorkSite(p *Package) map[string][]byte {
	entry, _ := WorkEntryOf(p.Bundle)
	prefix := strings.TrimSuffix(entry, SiteEntry)
	out := map[string][]byte{}
	for _, f := range p.Bundle.Paths() {
		if f == ManifestFile {
			continue
		}
		data, _ := p.Bundle.File(f)
		if rel, ok := strings.CutPrefix(f, prefix); ok {
			out[rel] = data
		}
	}
	if prefix != "" {
		carry := []string{ReadmeFile}
		if c, found, err := tarbundle.SiteCover(p.Bundle); found && err == nil {
			carry = append(carry, c.File)
		}
		for _, f := range carry {
			if data, ok := p.Bundle.File(f); ok && out[f] == nil {
				out[f] = data
			}
		}
	}
	return out
}

// WorkFile is a single-file work's file: its name and bytes (ok=false for
// a site or an app).
func WorkFile(p *Package) (string, []byte, bool) {
	if wire.WorkTypeIsSite(p.Manifest.Type) {
		return "", nil, false
	}
	data, ok := p.Bundle.File(p.Manifest.File)
	return p.Manifest.File, data, ok
}

// WorkSnapshot is a deliverable version as a work holds it: a site's files
// (Site), or one file (Name and Data) with an optional cover image (Cover:
// a video's frame; a site's cover is its own cover file).
type WorkSnapshot struct {
	Site  *tarbundle.Bundle
	Name  string
	Data  []byte
	Cover []byte
}

// WorkFromDeliverable is the work a deliverable version publishes as: its
// files as they are with m as component.json (its kind, type and file set
// here, the type by WorkTypeOf). A site holding a component.json of its
// own is refused: the manifest is the work's. An HTML document becomes a
// site whose entry it is; a file named as a file the bundle reserves is
// renamed.
func WorkFromDeliverable(d WorkDeliverable, snap WorkSnapshot, m Manifest) (*Package, []byte, error) {
	files := map[string][]byte{}
	m.Kind, m.File = KindWork, ""
	if snap.Site != nil {
		if snap.Cover != nil {
			return nil, nil, errors.New("a site's cover is its own cover file: save it in the site's folder")
		}
		if _, ok := snap.Site.File(ManifestFile); ok {
			return nil, nil, fmt.Errorf("the site holds a %s of its own: rename it, then publish again", ManifestFile)
		}
		for _, f := range snap.Site.Paths() {
			files[f], _ = snap.Site.File(f)
		}
		m.Type = WorkTypeOf(d)
		return Pack(m, files, Options{})
	}
	name := path.Base(strings.TrimSpace(snap.Name))
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	d.FileName, d.Head = name, workHead(snap.Data)
	m.Type = WorkTypeOf(d)
	if wire.WorkTypeIsSite(m.Type) {
		if snap.Cover != nil {
			return nil, nil, errors.New("a site's cover is its own cover file: save it in the site's folder")
		}
		files[SiteEntry] = snap.Data
		return Pack(m, files, Options{})
	}
	if name == ManifestFile || name == ReadmeFile {
		name = workRenamedPrefix + name
	}
	if snap.Cover != nil {
		cover, err := tarbundle.CoverFile(snap.Cover)
		if err != nil {
			return nil, nil, fmt.Errorf("the cover: %w", err)
		}
		if cover == name {
			return nil, nil, fmt.Errorf("the work's file is named %s: it is its own cover", name)
		}
		files[cover] = snap.Cover
	}
	files[name], m.File = snap.Data, name
	return Pack(m, files, Options{})
}

// workFromForm: a work is published from a deliverable, never written in a
// form (nor imported from another format).
func workFromForm(Form, *Manifest, map[string][]byte) error {
	return errors.New("a work is published from a deliverable (Publish to gallery), not written in a form")
}
