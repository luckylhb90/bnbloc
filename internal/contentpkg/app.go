package contentpkg

import (
	"errors"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Apps (docs/prd/2026-10-13-app-gallery.md §3): an interactive app
// published as a template. The bundle is the site folder as published (a
// site deliverable's files: index.html at the root, or a build's dist/),
// within a site snapshot's caps, plus component.json; it may carry a card
// cover (tarbundle.SiteCover: cover.png, cover.webp, cover.jpg or
// cover.jpeg at the root) and a README.md for the detail page. A template
// holds no data: an app's data lives in its site's data space, which is
// never part of a bundle. The code runs only in the sandboxed site frame,
// never on a member's machine, so its scripts need no permission and an
// app requires nothing.

// App files.
const (
	// AppEntry is the document that makes a folder a site.
	AppEntry = "index.html"
	// appDistDir is a build's output folder, the other entry an app may
	// have (dist/index.html).
	appDistDir = "dist"
)

// AppLimits are an app bundle's caps: a site snapshot's plus its manifest.
var AppLimits = tarbundle.Limits{MaxBytes: tarbundle.SiteLimits.MaxBytes + maxAppManifest, MaxFiles: tarbundle.SiteLimits.MaxFiles + 1}

// maxAppManifest is the room an app's component.json takes beyond the
// site's own caps.
const maxAppManifest = 1 << 20

func init() {
	register(KindSpec{Kind: KindApp, Short: "app", Limits: AppLimits, Site: true,
		validate: validateApp, fromForm: appFromForm, fromImport: appFromImport})
}

// validateApp: an entry point, no protected workspace path, a cover (when
// it ships one) that tarbundle.SiteCover accepts (an image of the type its
// name says, within wire.MaxAppCoverBytes; an SVG is never one), a
// README.md that is text, a category from wire.AppCategories, and none of
// the other kinds' fields (an app requires nothing).
func validateApp(p *Package) error {
	m := p.Manifest
	switch {
	case len(m.QuickPrompts) > 0 || m.Welcome != "":
		return errors.New("quick prompts and a welcome belong to experts")
	case len(m.Skills) > 0:
		return errors.New("an app lists no skills")
	case len(m.Prompts) > 0:
		return errors.New("prompts belong to prompt packs")
	case len(m.Requires.Skills)+len(m.Requires.Prompts)+len(m.Requires.MCP)+len(m.Requires.Permissions) > 0:
		return errors.New("an app requires nothing: its code runs only in the sandboxed site frame")
	}
	if m.Category != "" && !wire.ValidAppCategory(m.Category) {
		return fmt.Errorf("category %q: an app's category is one of %s", m.Category, strings.Join(wire.AppCategories, ", "))
	}
	if _, ok := AppEntryOf(p.Bundle); !ok {
		return fmt.Errorf("an app needs %s at its root (or %s/%s)", AppEntry, appDistDir, AppEntry)
	}
	for _, f := range p.Bundle.Paths() {
		if core.ProtectedWorkspacePath(f) {
			return fmt.Errorf("%s: an app holds no agent configuration or room files", f)
		}
	}
	if _, _, err := tarbundle.SiteCover(p.Bundle); err != nil {
		return err
	}
	if _, ok := p.Bundle.File(ReadmeFile); ok {
		if _, err := readText(p.Bundle, ReadmeFile, maxInstructions); err != nil {
			return err
		}
	}
	return nil
}

// AppEntryOf is the entry point of a site folder: index.html at the root,
// else dist/index.html.
func AppEntryOf(b *tarbundle.Bundle) (string, bool) {
	for _, e := range []string{AppEntry, appDistDir + "/" + AppEntry} {
		if _, ok := b.File(e); ok {
			return e, true
		}
	}
	return "", false
}

// AppSite is the site a validated app copies into a room ("Use this app"):
// its files without the manifest, a dist/ entry's files at the root (with
// the root's cover and README.md beside them).
func AppSite(p *Package) map[string][]byte {
	entry, _ := AppEntryOf(p.Bundle)
	prefix := strings.TrimSuffix(entry, AppEntry)
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

// AppFromSite is the app a site snapshot publishes as (publishing from a
// site): the snapshot's files as they are, with m as component.json (its
// kind set here). A site holding a component.json of its own is refused:
// the manifest is the app's.
func AppFromSite(site *tarbundle.Bundle, m Manifest) (*Package, []byte, error) {
	if _, ok := site.File(ManifestFile); ok {
		return nil, nil, fmt.Errorf("the site holds a %s of its own: rename it, then publish again", ManifestFile)
	}
	files := make(map[string][]byte, len(site.Paths()))
	for _, f := range site.Paths() {
		files[f], _ = site.File(f)
	}
	m.Kind = KindApp
	return Pack(m, files, Options{})
}

// appFromForm: an app is published from a site, never written in a form.
func appFromForm(Form, *Manifest, map[string][]byte) error {
	return errors.New("an app is published from a site deliverable (Publish to gallery), not written in a form")
}

// appFromImport: an uploaded site folder (index.html or dist/) becomes the
// app's files as they are.
func appFromImport(files map[string][]byte, _ *Manifest, out map[string][]byte) error {
	for p, data := range files {
		if p != ReadmeFile {
			out[p] = data
		}
	}
	return nil
}
