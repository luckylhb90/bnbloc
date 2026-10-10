// Package previewerruntime is the extpipe lifecycle of previewer components
// (bnbloc.previewer.package/v1): static web bundles the web client runs in a
// sandboxed opaque-origin frame to render artifact files. Nothing runs
// server-side: install verifies the package and its bundle archive and
// persists the record, so the member catalog (GET /v1/components, rollout +
// tenant gated) can hand clients the match/toolbar metadata and the bundle,
// and the gateway can serve the bundle files to browser frames.
package previewerruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// BundleProbe checks a previewer record's bundle artifact against the
// gateway artifact store (present, sha256, archive valid, entry present).
// The gateway supplies it: only it can read the blob store.
type BundleProbe func(ctx context.Context, rec extpipe.Record, cfg extpipe.PackageConfig) error

// Installer returns the previewer kind's lifecycle vtable.
func Installer(probeBundle BundleProbe) extpipe.KindInstaller {
	return extpipe.KindInstaller{
		Kind:         extreg.KindPreviewer,
		PackageKinds: []string{pkgmeta.KindPreviewerPackageV1},
		Decode:       DecodePackage,
		Probe: func(ctx context.Context, rec extpipe.Record, _ extgov.Guard) error {
			cfg, err := ValidateRecord(rec)
			if err != nil {
				return err
			}
			if probeBundle == nil {
				return nil
			}
			return probeBundle(ctx, rec, cfg)
		},
		// Activate registers nothing: a previewer has no live driver. A nil
		// instance keeps the record out of the live registry while the
		// pipeline still tracks it for reconcile.
		Activate: func(context.Context, extpipe.Record, extgov.Guard) (any, error) { return nil, nil },
	}
}

// DecodePackage projects a verified previewer package onto its persisted config.
func DecodePackage(pkg pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
	raw, err := extpipe.EncodePackageConfig(pkg)
	if err != nil {
		return nil, "", err
	}
	return raw, extreg.BackendBundle, nil
}

// ValidateRecord re-validates a previewer record exactly as the install gate
// validates a package and returns its decoded config.
func ValidateRecord(rec extpipe.Record) (extpipe.PackageConfig, error) {
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		return extpipe.PackageConfig{}, err
	}
	if err := pkgmeta.ValidatePackage(cfg.Package(pkgmeta.KindPreviewerPackageV1, rec)); err != nil {
		return extpipe.PackageConfig{}, fmt.Errorf("previewer record invalid: %w", err)
	}
	return cfg, nil
}

// Bundle returns a validated previewer config's pinned bundle artifact.
func Bundle(cfg extpipe.PackageConfig) (pkgmeta.Artifact, bool) {
	art, ok := cfg.Artifacts[pkgmeta.PreviewerBundleArtifact]
	return art, ok && cfg.Previewer != nil
}
