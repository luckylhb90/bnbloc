package gateway

import (
	"context"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

func legacyManifestPackage(manifest manifestcore.Manifest) pkgmeta.Package {
	return pkgmeta.Package{
		Kind:      pkgmeta.KindCorePackageV1,
		Name:      manifest.Name,
		Version:   "0.0.0",
		Publisher: "local",
		Protocol: protocol.ProtocolRange{
			Min: protocol.SupportedMinVersion,
			Max: protocol.SupportedMaxVersion,
		},
		Runtime:  pkgmeta.RuntimeManifest,
		Manifest: &manifest,
	}
}

// legacyManifestInstallPipeline installs a bare manifest core unsigned and
// without the live conformance probe (tests have no daemon to probe). The
// probe is replaced on the TEST pipeline only; production has no flag to skip
// it.
func (s *Service) legacyManifestInstallPipeline() *extpipe.Pipeline {
	base := s.corePipeline()
	installer := s.coreInstaller()
	installer.Probe = func(context.Context, extpipe.Record, extgov.Guard) error { return nil }
	return &extpipe.Pipeline{
		Settings:       base.Settings,
		Audit:          base.Audit,
		AllowUnsigned:  true,
		Installers:     map[extreg.Kind]extpipe.KindInstaller{core.KindCore: installer},
		TrustedKeysKey: base.TrustedKeysKey,
		Now:            base.Now,
	}
}
