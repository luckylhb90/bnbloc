package gateway

import (
	"context"
	"log"

	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// RehydrateInstalledComponents re-activates every component installed by a
// super-admin from durable platform settings (MySQL), so installs survive a
// gateway restart with no disk state. Activation puts each drivable
// (non-sidecar) installed core into the service's core list through
// coreset.EntryFor; the composition root adds the env-configured cores after
// it (Service.AddEnvCores). Best-effort: one bad record is audited and
// skipped, never fatal. A nil settings store (dev/in-memory) does nothing.
func RehydrateInstalledComponents(ctx context.Context, pipe *extpipe.Pipeline, host extreg.Service) {
	if pipe == nil || pipe.Settings == nil {
		return
	}
	if err := pipe.Rehydrate(ctx, host); err != nil {
		log.Printf("bnbloc-gateway: component rehydrate: load failed: %v", err)
	}
}
