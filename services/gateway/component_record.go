package gateway

import (
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func componentFromExtRecord(rec extpipe.Record) (wire.PlatformComponent, error) {
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		return wire.PlatformComponent{}, err
	}
	out := wire.PlatformComponent{
		Kind:            rec.Kind,
		Name:            rec.Name,
		Version:         rec.Version,
		Publisher:       rec.Publisher,
		Runtime:         string(cfg.Runtime),
		Contract:        cfg.Contract,
		Protocol:        componentProtocol(cfg.Protocol),
		KeyID:           rec.KeyID,
		ContentHash:     rec.ContentHash,
		InstalledAtMs:   rec.InstalledAtMs,
		Description:     cfg.Description,
		Capabilities:    cfg.Capabilities,
		Permissions:     cfg.EffectivePermissions(rec),
		Hooks:           cfg.Hooks,
		Settings:        cfg.Settings,
		Artifacts:       cfg.Artifacts,
		Endpoint:        cfg.Endpoint,
		Sidecar:         cfg.Sidecar,
		RequiresDesktop: componentRequiresDesktop(cfg.Runtime, rec.Kind),
		Previewer:       cfg.Previewer,
		MCP:             cfg.MCP,
		Rollout:         extpipe.NormalizeRollout(rec.Rollout),
		Trust:           wire.ComponentTrust{KeyID: rec.KeyID, ContentHash: rec.ContentHash, Signed: rec.KeyID != ""},
	}
	if cfg.Manifest != nil {
		out.Manifest = cfg.Manifest
	}
	return out, nil
}

// componentProtocol is the projection of a package's protocol range.
func componentProtocol(p protocol.ProtocolRange) wire.ComponentProtocolRange {
	return wire.ComponentProtocolRange{Min: p.Min, Max: p.Max}
}

func componentRequiresDesktop(runtime pkgmeta.Runtime, kind string) bool {
	return (runtime == pkgmeta.RuntimeSidecar && kind != "auth-provider") || runtime == pkgmeta.RuntimeStdio
}
