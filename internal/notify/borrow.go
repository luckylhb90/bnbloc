package notify

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// BorrowRequested notifies an agent owner that a borrower asked for more runs
// on the owner's room authorization (a borrow quota request). It returns at
// once: the enqueue runs detached, bounded, and best-effort, so the request
// write never waits on (or fails because of) notification work.
func (e *Emitter) BorrowRequested(ctx context.Context, q store.QuotaRequest) {
	if e == nil {
		return
	}
	tid := q.TenantID
	if tid == "" {
		tid = tenant.FromContext(ctx)
	}
	text := strings.TrimSpace(q.Note)
	if q.RequestedRuns != nil {
		runs := "Requested runs: " + strconv.Itoa(*q.RequestedRuns) + "."
		if text == "" {
			text = runs
		} else {
			text = runs + " " + text
		}
	}
	if text == "" {
		text = "Approve or decline the request in the room."
	}
	ev := Event{
		Type: wire.NotificationBorrowRequest, RoomID: q.RoomID, Recipients: []string{q.OwnerUserID},
		Actor: q.RequesterUserID, Title: q.RequesterUserID + " asked for more runs on your " + q.Provider + " agent",
		Text: text, DedupKey: "quota:" + q.RequestID,
	}
	go func() {
		ctx, cancel := context.WithTimeout(tenant.NewContext(context.WithoutCancel(ctx), tid), 10*time.Second)
		defer cancel()
		if _, err := e.Emit(ctx, ev); err != nil {
			slog.Warn("bnbloc notifications: borrow request enqueue failed",
				"event", "bnbloc.notify.enqueue_failed", "type", ev.Type, "room", ev.RoomID, "error", err)
		}
	}()
}
