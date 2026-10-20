package main

import (
	"flag"
	"fmt"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

// A component's own settings page (docs/architecture/settings-pages.md):
// `cores publish-mcp` and `components publish-package` take its bundle as
// --settings-page <dir or tar>, checked like a previewer's (frameBundleInput)
// before anything is uploaded, then uploaded and pinned in the package's
// settingsPage.bundle like a previewer's bundle.

// addSettingsPageFlag declares --settings-page on a publish command.
func addSettingsPageFlag(fs *flag.FlagSet) *string {
	return fs.String("settings-page", "", "the component's own settings page: its built directory or bundle archive (.tar), holding the package's settingsPage.entry")
}

// settingsPageInput reads --settings-page for a package before any upload:
// the archive's path ("" when the package's page is already pinned or it
// has none). A page declared but not pinned needs the flag; the flag needs
// the declaration (its entry). cleanup removes a temporary archive.
func settingsPageInput(pkg pkgmeta.Package, src string) (string, func(), error) {
	noop := func() {}
	page := pkg.SettingsPage
	switch {
	case src == "" && page != nil && page.Bundle == (pkgmeta.Artifact{}):
		return "", noop, fmt.Errorf("the package declares a settingsPage: pass its bundle with --settings-page <dir or tar>")
	case src == "":
		return "", noop, nil
	case page == nil:
		return "", noop, fmt.Errorf(`--settings-page needs the package's settingsPage ({"entry": "index.html", "bundle": {"url": "", "sha256": ""}})`)
	}
	path, cleanup, err := frameBundleInput(src, page.Entry)
	if err != nil {
		return "", noop, fmt.Errorf("--settings-page: %w", err)
	}
	return path, cleanup, nil
}

// pinSettingsPage uploads the page bundle settingsPageInput read and pins it
// in the package (nothing when path is "").
func pinSettingsPage(c *client, pkg *pkgmeta.Package, path string) error {
	if path == "" {
		return nil
	}
	up, err := uploadArtifactFile(c, path)
	if err != nil {
		return fmt.Errorf("upload settings page: %w", err)
	}
	pkg.SettingsPage.Bundle = pkgmeta.Artifact{URL: up.URL, SHA256: up.SHA256}
	return nil
}
