package contentpkg

import (
	"fmt"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

// Platform scope (docs/prd/2026-10-12-experts-and-teams.md §1, step 4):
// content reaches every organization the way executable components do, by
// a package a super admin signs offline and installs through the existing
// pipeline (bnbloc cores install --package … --signature …, POST
// /v1/platform/components). The package is the pkgmeta envelope of the
// content kind: it names the version and carries one artifact, the
// version's bundle on the gateway (/v1/artifacts/sha256/<hex>), pinned by
// its sha256, so the signature covers the exact bytes members will get.
// The bundle itself is validated again by this package's validator when
// it is installed.

// PlatformBundleArtifact is the one artifact key of a content platform
// package.
const PlatformBundleArtifact = "bundle"

// PlatformPackage is the signable package of one version of a content
// component whose bundle is at bundleURL.
func PlatformPackage(m Manifest, bundleSHA256, bundleURL, publisher string) pkgmeta.Package {
	return pkgmeta.Package{
		Kind: m.Kind, Name: m.Name, Version: m.Version, Runtime: pkgmeta.RuntimeBundle,
		Publisher: publisher, Description: m.Description,
		Artifacts: map[string]pkgmeta.Artifact{PlatformBundleArtifact: {URL: bundleURL, SHA256: bundleSHA256}},
	}
}

// IsContentKind reports whether a package kind is a content kind.
func IsContentKind(kind string) bool {
	_, ok := kinds[kind]
	return ok
}

// validatePlatformPackage is the pkgmeta payload validator of the content
// kinds: a bundle runtime with exactly the bundle artifact and nothing an
// executable component declares.
func validatePlatformPackage(pkg pkgmeta.Package) error {
	switch {
	case !ValidName(pkg.Name):
		return fmt.Errorf("content package name %q must use lowercase letters, digits and hyphens", pkg.Name)
	case pkg.Runtime != pkgmeta.RuntimeBundle:
		return fmt.Errorf("content package runtime must be %q", pkgmeta.RuntimeBundle)
	case pkg.Endpoint != nil || pkg.Sidecar != nil || pkg.Manifest != nil || pkg.MCP != nil || pkg.Previewer != nil:
		return fmt.Errorf("a content package carries only its bundle")
	case pkg.Contract != "" || len(pkg.Hooks) > 0 || len(pkg.Capabilities) > 0:
		return fmt.Errorf("a content package declares no contract, hooks or capabilities")
	case pkg.Protocol.Min != "" || pkg.Protocol.Max != "":
		return fmt.Errorf("a content package declares no protocol")
	case len(pkg.Permissions.Network) > 0:
		return fmt.Errorf("a content package gets no network access; its declared permissions are in component.json")
	}
	a, ok := pkg.Artifacts[PlatformBundleArtifact]
	if len(pkg.Artifacts) != 1 || !ok {
		return fmt.Errorf("a content package has exactly one artifact %q", PlatformBundleArtifact)
	}
	if a.URL == "" || a.SHA256 == "" {
		return fmt.Errorf("artifacts.%s needs the bundle's url and sha256 (export it from the gateway)", PlatformBundleArtifact)
	}
	return nil
}
