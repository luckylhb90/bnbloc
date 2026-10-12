package gateway

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// componentsChanged tells realtime that a member's executable component
// catalog (GET /v1/components) changed, so realtime pushes a
// components_changed hint to the member desktops it concerns and they
// refresh now instead of at their next poll (the poll stays the safety
// net). tenantID and member narrow it ("" = every organization / member):
// a platform install, uninstall, rollout or tenant change concerns
// everyone, an opt-in install or removal one member. Best-effort: the
// change already succeeded, a failed signal is logged by the publisher.
// Content components never signal: no desktop runs them.
func (s *Service) componentsChanged(ctx context.Context, tenantID, member string) {
	if s.Backends.Realtime == "" {
		return
	}
	realtime.NewHTTPPublisher(s.Backends.Realtime, "gateway").ComponentsChanged(ctx, wire.ComponentsChanged{
		TenantID: tenantID, Member: member, AtMs: s.nowTime().UnixMilli(),
	})
}
