package gateway

import (
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
)

func componentFromExtRecord(rec extpipe.Record) (installedComponent, error) {
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		return installedComponent{}, err
	}
	return installedComponent{
		Kind:            rec.Kind,
		Name:            rec.Name,
		Version:         rec.Version,
		Publisher:       rec.Publisher,
		Runtime:         cfg.Runtime,
		Contract:        cfg.Contract,
		Protocol:        cfg.Protocol,
		KeyID:           rec.KeyID,
		ContentHash:     rec.ContentHash,
		InstalledAt:     rec.InstalledAtMs,
		Description:     cfg.Description,
		Capabilities:    cfg.Capabilities,
		Permissions:     cfg.EffectivePermissions(rec),
		Hooks:           cfg.Hooks,
		LocalSettings:   cfg.LocalSettings,
		Artifacts:       cfg.Artifacts,
		Endpoint:        cfg.Endpoint,
		Sidecar:         cfg.Sidecar,
		RequiresDesktop: componentRequiresDesktop(cfg.Runtime, rec.Kind),
		Manifest:        cfg.Manifest,
		Previewer:       cfg.Previewer,
		Rollout:         extpipe.NormalizeRollout(rec.Rollout),
		Trust:           componentTrust{KeyID: rec.KeyID, ContentHash: rec.ContentHash, Signed: rec.KeyID != ""},
	}, nil
}

func componentRequiresDesktop(runtime pkgmeta.Runtime, kind string) bool {
	return (runtime == pkgmeta.RuntimeSidecar && kind != "auth-provider") || runtime == pkgmeta.RuntimeStdio
}
