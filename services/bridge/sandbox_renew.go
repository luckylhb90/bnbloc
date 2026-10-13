package bridge

import (
	"context"
	"log/slog"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// A sandboxed turn's command access renews itself
// (docs/prd/2026-10-13-live-turns.md §2): its exec token is short-lived, and
// while the turn runs the bridge mints a fresh one every
// sandboxExecRenewEvery and hands it to the running session (Core Protocol
// PUT …/sessions/{sid}/execution, capability executionRefresh), so a turn may
// run for hours and a token lapses within the hour after it ends.

// execRenewEvery is how often a running turn's token is renewed
// (Bridge.execRenewInterval overrides it in tests).
func (b *Bridge) execRenewEvery() time.Duration {
	if b.execRenewInterval > 0 {
		return b.execRenewInterval
	}
	return sandboxExecRenewEvery
}

// renewExecution renews the exec token of the sandboxed turn running in
// session sessionID until the returned stop is called (the turn ended). A
// failed renewal never fails the turn: it is logged and retried at the next
// tick. On a core without executionRefresh it does nothing (the turn's
// token was minted to outlive it, execTokenTTL).
func (b *Bridge) renewExecution(ctx context.Context, d *drive, workspaceID, sessionID string, exec *corepkg.Execution) (stop func()) {
	entry, ok := b.coreEntry(d.core)
	if d.caps.refresher == nil || exec == nil || b.sandbox == nil || !ok {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(b.execRenewEvery())
		defer t.Stop()
		current := exec
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			next, err := b.sandbox.renewed(ctx, current, sandboxExecTokenTTL)
			if err == nil {
				rctx, rcancel := context.WithTimeout(ctx, corePollTimeout)
				err = d.caps.refresher.RefreshExecution(rctx, entry.endpoint, workspaceID, sessionID, next)
				rcancel()
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("bnbloc bridge could not renew a sandboxed turn's command access",
					"event", "bnbloc.bridge.exec_renew_failed", "room", d.job.RoomID, "core", d.core, "session", sessionID, "error", err)
				continue
			}
			current = next
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
