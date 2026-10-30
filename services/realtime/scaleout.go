package realtimehttp

import (
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Scale-out without owners (docs/prd/2026-10-30-replicas-without-owners.md):
// every replica serves every room and member. A room's events reach the
// replicas streaming it over the hub's bus (internal/realtime: the order,
// the reorder hold, the replay buffer); presence and the roster are joined
// across replicas by RoomViews; slow mode is a claim every replica shares;
// what a member's desktop plugin stream needs crosses on Replicas
// (replicas.go). Store-backed reads and the lease-claiming workers
// (notification outbox, scheduler) need nothing more.

func (s *Service) registerReplicaRoutes(mux *http.ServeMux, catalog func(http.HandlerFunc) http.Handler) {
	mux.Handle("POST /internal/v1/components/changed", catalog(s.handleInternalComponentsChanged))
}

// handleInternalComponentsChanged queues a catalog hint
// (wire.ComponentsChanged) on the member streams this replica holds and
// hands it to every other replica for theirs. The gateway calls it after a
// change of the executable component catalog.
func (s *Service) handleInternalComponentsChanged(w http.ResponseWriter, r *http.Request) {
	var c wire.ComponentsChanged
	if !httpx.DecodeJSON(w, r, maxSmallBodyBytes, &c) {
		return
	}
	c.TenantID, c.Member = strings.TrimSpace(c.TenantID), strings.TrimSpace(c.Member)
	if c.AtMs <= 0 {
		c.AtMs = time.Now().UnixMilli()
	}
	s.PluginHooks.componentsChanged(c)
	s.Replicas.broadcastCatalog(c)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- member desktop route

// MemberDesktopRoute is the notification worker's DesktopRoute. The outbox
// worker runs on every replica (lease claims), and a member's desktop
// plugin stream may be held by any of them: a notification for a member
// whose desktop is elsewhere is delivered there (Replicas). A replica alone
// (nil replicas) is the local dispatcher itself.
func MemberDesktopRoute(local *PluginHookDispatcher, replicas *Replicas) notify.DesktopRoute {
	if replicas == nil {
		return local
	}
	return &memberDesktop{local: local, replicas: replicas}
}

type memberDesktop struct {
	local    *PluginHookDispatcher
	replicas *Replicas
}

// HasDesktop is true when this replica or another holds the member's
// desktop stream; an unreadable mark answers false, which routes the member
// to the durable server notifiers.
func (m *memberDesktop) HasDesktop(tenantID, member string) bool {
	return m.local.HasDesktop(tenantID, member) || m.replicas.hasDesktop(tenantID, member)
}

// DeliverNotification delivers here when the member's desktop is here,
// else through the replica holding it; done reports the outcome, or false
// when none answers (the worker then queues the server fallback).
func (m *memberDesktop) DeliverNotification(tenantID string, n wire.PluginNotification, done func(delivered bool)) {
	if m.local.HasDesktop(tenantID, n.UserID) {
		m.local.DeliverNotification(tenantID, n, done)
		return
	}
	m.replicas.deliverNotification(tenantID, n, done)
}
