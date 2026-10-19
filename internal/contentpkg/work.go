package contentpkg

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/semver"
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
// detail page, and the recipe that made it (the manifest's recipe, its
// reference files under references/). A work holds no data: an app's data
// lives in its site's data space, which is never part of a bundle. Nothing
// in it runs on a member's machine (a site's code runs only in the
// sandboxed site frame), so its scripts need no permission and a work
// requires nothing.

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
	// WorkReferencesDir holds a recipe's reference files.
	WorkReferencesDir = "references"
)

// WorkLimits are a work bundle's caps: a site snapshot's (a single file is
// held to the same bytes), its recipe's references and its manifest and
// cover.
var WorkLimits = tarbundle.Limits{
	MaxBytes: tarbundle.SiteLimits.MaxBytes + wire.MaxWorkReferenceBytes + wire.MaxWorkCoverBytes + maxWorkManifest,
	MaxFiles: tarbundle.SiteLimits.MaxFiles + wire.MaxWorkReferences + 2,
}

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
	if err := validateRecipe(p); err != nil {
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
// manifest, a README.md, a cover and the recipe's references.
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
	refs := referenceFiles(m)
	for _, f := range p.Bundle.Paths() {
		switch {
		case f == ManifestFile, f == ReadmeFile, f == m.File, f == cover.File, refs[f]:
		default:
			return fmt.Errorf("%s: a %s work holds only its file, %s, %s, a cover and its recipe's references", f, m.Type, ManifestFile, ReadmeFile)
		}
	}
	return nil
}

// RecipeEmpty reports a recipe that says nothing of how a work was made:
// none, or no request, expert, pack or reference (the agent alone is
// informational). A publish stores no empty recipe; "Make similar" attaches
// the work itself for one.
func RecipeEmpty(r *wire.WorkRecipe) bool {
	return r == nil || (r.Prompt == "" && r.Expert == nil && len(r.Skills)+len(r.Prompts)+len(r.References) == 0)
}

// referenceFiles are the bundle paths of a manifest's recipe references.
func referenceFiles(m Manifest) map[string]bool {
	out := map[string]bool{}
	if m.Recipe != nil {
		for _, r := range m.Recipe.References {
			out[r.File] = true
		}
	}
	return out
}

// Bounds of a recipe's text.
const (
	maxRecipePartID   = 64
	maxReferenceName  = 255
	maxReferenceType  = 127
	maxRecipeAgentRef = 64
)

// validateRecipe: the prompt is chat text (wire.MaxChatTextBytes), each
// component is an id, a valid name and a semantic version, at most
// maxRequires of each kind, and each reference is a file of the bundle
// under references/ of its stated size, at most wire.MaxWorkReferences
// within wire.MaxWorkReferenceBytes together. A stored recipe names no
// attachment (that is a capture's, before publishing).
func validateRecipe(p *Package) error {
	r := p.Manifest.Recipe
	if r == nil {
		return nil
	}
	if !utf8.ValidString(r.Prompt) || len(r.Prompt) > wire.MaxChatTextBytes {
		return fmt.Errorf("recipe prompt: UTF-8 text of at most %d bytes", wire.MaxChatTextBytes)
	}
	parts := append([]wire.WorkRecipePart(nil), r.Skills...)
	parts = append(parts, r.Prompts...)
	if r.Expert != nil {
		parts = append(parts, *r.Expert)
	}
	if len(r.Skills) > maxRequires || len(r.Prompts) > maxRequires {
		return fmt.Errorf("recipe: at most %d skill packs and %d prompt packs", maxRequires, maxRequires)
	}
	for _, c := range parts {
		switch {
		case c.ID == "" || len(c.ID) > maxRecipePartID || strings.ContainsAny(c.ID, "/ \r\n"):
			return fmt.Errorf("recipe component %q: an id", c.ID)
		case !ValidName(c.Name):
			return fmt.Errorf("recipe component %q: name %q", c.ID, c.Name)
		case c.Version != "" && !semver.Valid(c.Version):
			return fmt.Errorf("recipe component %s: version %q", c.Name, c.Version)
		}
		if err := checkText("recipe displayName", c.DisplayName, maxDisplayName, true); err != nil {
			return err
		}
	}
	if a := r.Agent; a != nil {
		if a.Provider == "" {
			return errors.New("recipe agent: a provider")
		}
		for _, s := range []string{a.Core, a.Provider} {
			if err := checkText("recipe agent", s, maxRecipeAgentRef, true); err != nil {
				return err
			}
		}
	}
	if len(r.References) > wire.MaxWorkReferences {
		return fmt.Errorf("recipe: at most %d reference files", wire.MaxWorkReferences)
	}
	var total int64
	seen := map[string]bool{}
	for _, ref := range r.References {
		dir, base := path.Split(ref.File)
		data, ok := p.Bundle.File(ref.File)
		switch {
		case ref.AttachmentID != "":
			return fmt.Errorf("recipe reference %s: a stored recipe names no attachment", ref.Name)
		case dir != WorkReferencesDir+"/" || base == "" || seen[ref.File]:
			return fmt.Errorf("recipe reference %q: a file of its own under %s/", ref.File, WorkReferencesDir)
		case !ok:
			return fmt.Errorf("recipe reference %s is not in the bundle", ref.File)
		case int64(len(data)) != ref.SizeBytes:
			return fmt.Errorf("recipe reference %s: %d bytes, not %d", ref.File, len(data), ref.SizeBytes)
		}
		seen[ref.File] = true
		total += ref.SizeBytes
		if err := checkText("recipe reference name", ref.Name, maxReferenceName, true); err != nil {
			return err
		}
		if err := checkText("recipe reference contentType", ref.ContentType, maxReferenceType, true); err != nil {
			return err
		}
	}
	if total > wire.MaxWorkReferenceBytes {
		return fmt.Errorf("recipe: the reference files are larger than %d bytes together", wire.MaxWorkReferenceBytes)
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
// ("Use this app") and runs as its demo: its files without the manifest
// and the recipe's references, a dist/ entry's files at the root (with the
// root's cover and README.md beside them).
func WorkSite(p *Package) map[string][]byte {
	entry, _ := WorkEntryOf(p.Bundle)
	prefix := strings.TrimSuffix(entry, SiteEntry)
	refs := referenceFiles(p.Manifest)
	out := map[string][]byte{}
	for _, f := range p.Bundle.Paths() {
		if f == ManifestFile || refs[f] {
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

// WorkReferenceFile is one reference file of a recipe: what the recipe
// says of it and its bytes.
type WorkReferenceFile struct {
	Ref  wire.WorkReference
	Data []byte
}

// WorkReferences are a validated work's reference files, in the recipe's
// order.
func WorkReferences(p *Package) []WorkReferenceFile {
	var out []WorkReferenceFile
	if r := p.Manifest.Recipe; r != nil {
		for _, ref := range r.References {
			data, _ := p.Bundle.File(ref.File)
			out = append(out, WorkReferenceFile{Ref: ref, Data: data})
		}
	}
	return out
}

// WorkSnapshot is a deliverable version as a work holds it: a site's files
// (Site), or one file (Name and Data) with an optional cover image (Cover:
// a video's frame; a site's cover is its own cover file), and the
// recipe's reference files (References: their names, types and bytes).
type WorkSnapshot struct {
	Site       *tarbundle.Bundle
	Name       string
	Data       []byte
	Cover      []byte
	References []WorkReferenceFile
}

// WorkFromDeliverable is the work a deliverable version publishes as: its
// files as they are with m as component.json (its kind, type and file set
// here, the type by WorkTypeOf). A site holding a component.json of its
// own is refused: the manifest is the work's. An HTML document becomes a
// site whose entry it is; a file named as a file the bundle reserves is
// renamed. With a recipe (m.Recipe), the snapshot's references go under
// references/ (each a name of its own) and the recipe lists them.
func WorkFromDeliverable(d WorkDeliverable, snap WorkSnapshot, m Manifest) (*Package, []byte, error) {
	files := map[string][]byte{}
	m.Kind, m.File = KindWork, ""
	if snap.Site != nil {
		if _, ok := snap.Site.File(ManifestFile); ok {
			return nil, nil, fmt.Errorf("the site holds a %s of its own: rename it, then publish again", ManifestFile)
		}
		for _, f := range snap.Site.Paths() {
			files[f], _ = snap.Site.File(f)
		}
		if _, own, _ := tarbundle.SiteCover(snap.Site); !own {
			if err := placeCover(files, snap.Cover); err != nil {
				return nil, nil, err
			}
		}
		m.Type = WorkTypeOf(d)
		m.Recipe = placeReferences(m.Recipe, snap.References, files)
		return Pack(m, files, Options{})
	}
	name := path.Base(strings.TrimSpace(snap.Name))
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	d.FileName, d.Head = name, workHead(snap.Data)
	m.Type = WorkTypeOf(d)
	if wire.WorkTypeIsSite(m.Type) {
		if err := placeCover(files, snap.Cover); err != nil {
			return nil, nil, err
		}
		files[SiteEntry] = snap.Data
		m.Recipe = placeReferences(m.Recipe, snap.References, files)
		return Pack(m, files, Options{})
	}
	if name == ManifestFile || name == ReadmeFile {
		name = workRenamedPrefix + name
	}
	files[name], m.File = snap.Data, name
	if snap.Cover != nil {
		cover, err := tarbundle.CoverFile(snap.Cover)
		if err != nil {
			return nil, nil, fmt.Errorf("the cover: %w", err)
		}
		if cover != name {
			files[cover] = snap.Cover
		}
	}
	m.Recipe = placeReferences(m.Recipe, snap.References, files)
	return Pack(m, files, Options{})
}

// placeCover puts a site's given cover (nil: none) at its root under the
// cover name its bytes sniff as (docs/prd/2026-10-19-work-covers.md §2.1:
// a single HTML file could carry none before).
func placeCover(files map[string][]byte, cover []byte) error {
	if cover == nil {
		return nil
	}
	name, err := tarbundle.CoverFile(cover)
	if err != nil {
		return fmt.Errorf("the cover: %w", err)
	}
	files[name] = cover
	return nil
}

// placeReferences puts the reference files into files under references/,
// each under its own name (a taken one gets a number), and returns the
// recipe listing them (a copy; nil stays nil, and its references go
// nowhere).
func placeReferences(r *wire.WorkRecipe, refs []WorkReferenceFile, files map[string][]byte) *wire.WorkRecipe {
	if r == nil {
		return nil
	}
	out := *r
	out.References = nil
	for _, f := range refs {
		base := safeReferenceName(f.Ref.Name)
		p := WorkReferencesDir + "/" + base
		for n := 2; files[p] != nil; n++ {
			p = fmt.Sprintf("%s/%d-%s", WorkReferencesDir, n, base)
		}
		files[p] = f.Data
		ref := f.Ref
		ref.File, ref.AttachmentID, ref.SizeBytes = p, "", int64(len(f.Data))
		out.References = append(out.References, ref)
	}
	return &out
}

// safeReferenceName is a reference's file name in the bundle: its base
// name with anything but letters, digits, '.', '-' and '_' replaced.
func safeReferenceName(name string) string {
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	b := []byte(base)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			b[i] = '-'
		}
	}
	out := strings.TrimLeft(string(b), ".")
	if out == "" {
		out = "file"
	}
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	return out
}

// workFromForm: a work is published from a deliverable, never written in a
// form (nor imported from another format).
func workFromForm(Form, *Manifest, map[string][]byte) error {
	return errors.New("a work is published from a deliverable (Publish to gallery), not written in a form")
}
