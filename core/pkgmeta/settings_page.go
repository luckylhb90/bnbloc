package pkgmeta

import (
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A component's own settings page (docs/prd/2026-10-19-component-settings.md
// §2.2a, docs/architecture/settings-pages.md): an HTML file of a static
// bundle the platform shows in a sandboxed frame instead of the one form.
// The bundle is a previewer bundle in all but its use: the same archive
// format and limits, served the same way, with the same policy (no network,
// an opaque origin). The declaration stays the contract: the page edits only
// the settings the package declares, through the platform.

// SettingsPage names the page for package authors (internal/wire owns its
// JSON).
type SettingsPage = wire.ComponentSettingsPage

// SettingsPageBundle is where a package pins its settings page's bundle
// (its place among Pins).
const SettingsPageBundle = "settingsPage.bundle"

// ValidBundleEntry is the rule for the document a bundle opens with (a
// previewer's entry, a settings page's): a bundle-relative .html path.
func ValidBundleEntry(entry string) bool {
	return core.ValidBundlePath(entry) && strings.HasSuffix(entry, ".html")
}

// validateSettingsPage checks a package's settings page (validatePackage
// runs it for every kind; the field-to-kinds table says which kinds may
// declare one): only beside settings, an entry, and a bundle pinned like an
// artifact (an upload placeholder until the publisher uploads it).
func validateSettingsPage(pkg Package) error {
	p := pkg.SettingsPage
	if p == nil {
		return nil
	}
	if len(pkg.Settings) == 0 {
		return fmt.Errorf("settingsPage needs settings: the page edits the settings the package declares")
	}
	if !ValidBundleEntry(p.Entry) {
		return fmt.Errorf("settingsPage.entry must be a relative .html path inside the bundle")
	}
	return validateArtifact(SettingsPageBundle, p.Bundle)
}

// Pins is every artifact a package pins, by the place it is declared at:
// "artifacts.<key>" (a binary per platform, a previewer's bundle) and
// SettingsPageBundle. The install gate checks each alike (complete,
// gateway-uploaded, present and matching), and a member may download each
// of a component they may see.
func (pkg Package) Pins() map[string]Artifact {
	out := make(map[string]Artifact, len(pkg.Artifacts)+1)
	for key, a := range pkg.Artifacts {
		out["artifacts."+key] = a
	}
	if pkg.SettingsPage != nil {
		out[SettingsPageBundle] = pkg.SettingsPage.Bundle
	}
	return out
}
