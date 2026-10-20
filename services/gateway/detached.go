package gateway

import (
	"context"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// goDetached runs fn after the request that started it, so the answer
// never waits on it: in a context detached from the request's
// cancellation (its values kept) acting in tenantID, bounded by timeout.
// fn logs its own failures; nothing it does fails the request. It is the
// gateway's one way to do work past an answer (a content notification, a
// room source's changed-task notice); waitDetached waits for all of it.
func (s *Service) goDetached(ctx context.Context, tenantID string, timeout time.Duration, fn func(ctx context.Context)) {
	s.detached.Add(1)
	go func() {
		defer s.detached.Done()
		ctx, cancel := context.WithTimeout(tenant.NewContext(context.WithoutCancel(ctx), tenantID), timeout)
		defer cancel()
		fn(ctx)
	}()
}

// waitDetached waits for every goDetached run started so far (tests, and a
// shutdown that lets them finish).
func (s *Service) waitDetached() { s.detached.Wait() }
