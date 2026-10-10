package shareagenthttp

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// BorrowNotifier receives borrow requests that should reach the agent owner as
// a member notification (implemented by notify.Emitter). It must return
// promptly: the request write never waits on notification delivery.
type BorrowNotifier interface {
	BorrowRequested(ctx context.Context, q store.QuotaRequest)
}

// notifyQuotaRequest hands a newly created quota request to the notifier.
func (s *Service) notifyQuotaRequest(ctx context.Context, q store.QuotaRequest) {
	if s.Notifications != nil {
		s.Notifications.BorrowRequested(ctx, q)
	}
}
