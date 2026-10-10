package gateway

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// RunComponentReconcile reconciles installed components every
// extpipe.ReconcileInterval until ctx ends (the shared pipeline loop).
func (s *Service) RunComponentReconcile(ctx context.Context) {
	s.corePipeline().RunReconcile(ctx, extreg.ServiceGateway, extpipe.ReconcileInterval, "bnbloc-gateway")
}

// reconcileComponents runs one reconcile on this service's pipeline (the
// same path Install/Uninstall and boot rehydrate use) and logs any change.
func (s *Service) reconcileComponents(ctx context.Context) {
	s.corePipeline().ReconcileLogged(ctx, extreg.ServiceGateway, "bnbloc-gateway")
}
