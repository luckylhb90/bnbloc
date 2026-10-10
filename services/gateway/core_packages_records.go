package gateway

import (
	"context"
	"log"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// RehydrateInstalledComponents re-activates every component installed by a
// super-admin from durable platform settings (MySQL), so installs survive a
// gateway restart with no disk state, and returns the names of the drivable
// (non-sidecar) installed cores so the composition root activates them in the
// core list. It MUST run before coreset.FromEnv, which resolves each core name
// against the registry. Best-effort: one bad record is audited and skipped,
// never fatal. A nil settings store (dev/in-memory) yields nothing.
func RehydrateInstalledComponents(ctx context.Context, pipe *extpipe.Pipeline, host extreg.Service) []string {
	if pipe == nil || pipe.Settings == nil {
		return nil
	}
	if err := pipe.Rehydrate(ctx, host); err != nil {
		log.Printf("bnbloc-gateway: component rehydrate: load failed: %v", err)
		return nil
	}
	recs, err := pipe.List(ctx, core.KindCore)
	if err != nil {
		log.Printf("bnbloc-gateway: core rehydrate: load failed: %v", err)
		return nil
	}
	names := make([]string, 0, len(recs))
	for _, rec := range recs {
		ext, ok := core.Describe(rec.Name)
		if !ok || ext.Source != core.SourceInstalled {
			continue
		}
		if ext.Backend == core.BackendSidecar {
			continue
		}
		names = append(names, rec.Name)
	}
	if len(names) > 0 {
		log.Printf("bnbloc-gateway: rehydrated installed cores %v from platform settings", names)
	}
	return names
}
