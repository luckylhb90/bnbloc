package main

import (
	"flag"
	"fmt"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

// A component's own pages (docs/architecture/settings-pages.md, plugin-panels.md):
// `cores publish-mcp` and `components publish-package` take its bundle as
// --settings-page <dir or tar>, checked like a previewer's (frameBundleInput)
// before anything is uploaded, then uploaded and pinned in the package's
// settingsPage.bundle like a previewer's bundle.

// addSettingsPageFlag declares --settings-page on a publish command.
func addSettingsPageFlag(fs *flag.FlagSet) *string {
	return fs.String("settings-page", "", "the component's own settings page: its built directory or bundle archive (.tar), holding the package's settingsPage.entry")
}

// pageInput reads a page flag (--settings-page, --panel) for a package before
// any upload: the archive's path ("" when the package's page is already
// pinned or it has none). A page declared but not pinned needs the flag; the
// flag needs the declaration (its entry). field names the package's field
// and flagName the flag, in errors. cleanup removes a temporary archive.
func pageInput(field, flagName string, page *pkgmeta.Page, src string) (string, func(), error) {
	noop := func() {}
	switch {
	case src == "" && page != nil && page.Bundle == (pkgmeta.Artifact{}):
		return "", noop, fmt.Errorf("the package declares a %s: pass its bundle with --%s <dir or tar>", field, flagName)
	case src == "":
		return "", noop, nil
	case page == nil:
		return "", noop, fmt.Errorf(`--%s needs the package's %s ({"entry": "index.html", "bundle": {"url": "", "sha256": ""}})`, flagName, field)
	}
	path, cleanup, err := frameBundleInput(src, page.Entry)
	if err != nil {
		return "", noop, fmt.Errorf("--%s: %w", flagName, err)
	}
	return path, cleanup, nil
}

// settingsPageInput reads --settings-page.
func settingsPageInput(pkg pkgmeta.Package, src string) (string, func(), error) {
	return pageInput("settingsPage", "settings-page", pkg.SettingsPage, src)
}

// panelInput reads --panel.
func panelInput(pkg pkgmeta.Package, src string) (string, func(), error) {
	return pageInput("panel", "panel", pkg.Panel, src)
}

// pinPage uploads the page bundle pageInput read and pins it in the page
// (nothing when path is "").
func pinPage(c *client, page *pkgmeta.Page, path string) error {
	if path == "" {
		return nil
	}
	up, err := uploadArtifactFile(c, path)
	if err != nil {
		return fmt.Errorf("upload page bundle: %w", err)
	}
	page.Bundle = pkgmeta.Artifact{URL: up.URL, SHA256: up.SHA256}
	return nil
}

// pinSettingsPage pins the settings page bundle settingsPageInput read.
func pinSettingsPage(c *client, pkg *pkgmeta.Package, path string) error {
	if path == "" {
		return nil
	}
	return pinPage(c, pkg.SettingsPage, path)
}

// pinPanel pins the panel bundle panelInput read.
func pinPanel(c *client, pkg *pkgmeta.Package, path string) error {
	if path == "" {
		return nil
	}
	return pinPage(c, pkg.Panel, path)
}

// addPanelFlag declares --panel on a publish command.
func addPanelFlag(fs *flag.FlagSet) *string {
	return fs.String("panel", "", "the plugin's own panel: its built directory or bundle archive (.tar), holding the package's panel.entry")
}
