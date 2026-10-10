package pluginruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// Config is a plugin's persisted config: the shared package projection every
// extension kind stores (extpipe.PackageConfig). Plugins use runtime,
// contract, hooks, sidecar, artifacts and permissions.
type Config = extpipe.PackageConfig

func Installer(activate func(context.Context, extpipe.Record, Config, extgov.Guard) (any, error), deactivate func(context.Context, extpipe.Record) error) extpipe.KindInstaller {
	if activate == nil {
		activate = func(context.Context, extpipe.Record, Config, extgov.Guard) (any, error) { return nil, nil }
	}
	return extpipe.KindInstaller{
		Kind:         pkgmeta.KindPlugin,
		PackageKinds: []string{pkgmeta.KindPluginPackageV1},
		Decode:       DecodePackage,
		Probe:        ProbeRecord,
		Activate: func(ctx context.Context, rec extpipe.Record, guard extgov.Guard) (any, error) {
			cfg, err := DecodeRecord(rec)
			if err != nil {
				return nil, err
			}
			return activate(ctx, rec, cfg, guard)
		},
		Deactivate: deactivate,
	}
}

// DecodePackage projects a verified plugin package onto its persisted config.
func DecodePackage(pkg pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
	raw, err := extpipe.EncodePackageConfig(pkg)
	if err != nil {
		return nil, "", err
	}
	return raw, extreg.BackendSidecar, nil
}

// DecodeRecord decodes a plugin record's persisted config.
func DecodeRecord(rec extpipe.Record) (Config, error) {
	return extpipe.DecodeConfig(rec)
}

// ProbeRecord checks a plugin record against the plugin contract. It does
// not dial the sidecar: the sidecar runs on member machines, supervised by
// their desktop, and realtime reaches it only through the member's plugin
// event stream (the desktop posts each envelope to its loopback localUrl).
func ProbeRecord(_ context.Context, rec extpipe.Record, _ extgov.Guard) error {
	cfg, err := DecodeRecord(rec)
	if err != nil {
		return err
	}
	if cfg.Runtime != pkgmeta.RuntimeSidecar {
		return fmt.Errorf(`plugin runtime must be "sidecar"`)
	}
	if cfg.Contract != pkgmeta.PluginContractV1 {
		return fmt.Errorf("unknown plugin contract %q", cfg.Contract)
	}
	for _, hook := range cfg.Hooks {
		if _, ok := pkgmeta.LookupHook(hook); !ok {
			return fmt.Errorf("unknown hook %q", hook)
		}
	}
	if cfg.Sidecar == nil || strings.TrimSpace(cfg.Sidecar.LocalURL) == "" {
		return fmt.Errorf("plugin sidecar requires sidecar.localUrl")
	}
	if err := pkgmeta.ValidateLoopbackHTTPURL(cfg.Sidecar.LocalURL); err != nil {
		return fmt.Errorf("plugin sidecar.localUrl must be a loopback http URL")
	}
	return nil
}

// HasHook reports whether the plugin config subscribes to hookID.
func HasHook(cfg Config, hookID string) bool {
	for _, hook := range cfg.Hooks {
		if hook == hookID {
			return true
		}
	}
	return false
}

// Subscribes reports whether rec is a plugin distributed to tenantID
// (rollout not off, tenant in scope) whose sidecar subscribes to hookID. It
// is the record half of hook entitlement; the member half (session tenant,
// opt-in install) is checked where the member's stream is known.
func Subscribes(rec extpipe.Record, hookID, tenantID string) bool {
	if extpipe.NormalizeRollout(rec.Rollout) == extpipe.RolloutOff || !rec.AllowsTenant(tenantID) {
		return false
	}
	cfg, err := DecodeRecord(rec)
	return err == nil && cfg.Sidecar != nil && HasHook(cfg, hookID)
}
