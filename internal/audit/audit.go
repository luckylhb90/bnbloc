// Package audit writes the shared audit trail (extension_audit): component
// lifecycle and deliveries, administration, session revocations, content
// uses, dispatches. Every writer goes through Record, so a failed write is
// handled one way and the outcome vocabulary is wire.AuditOutcome*
// (TestAuditThroughOneWriter).
package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Record appends rec to sink, best effort: the audited action already
// happened, so a failed write is logged (event bnbloc.audit.append_failed)
// and never surfaced to the caller. A nil sink records nothing. An empty
// Outcome is wire.AuditOutcomeOK; a zero AtMs is now.
func Record(ctx context.Context, sink store.ExtensionAuditStore, rec store.ExtAuditRecord) {
	if sink == nil {
		return
	}
	if rec.Outcome == "" {
		rec.Outcome = wire.AuditOutcomeOK
	}
	if rec.AtMs == 0 {
		rec.AtMs = time.Now().UnixMilli()
	}
	if err := sink.AppendExtAudit(ctx, rec); err != nil {
		slog.Warn("bnbloc audit append failed", "event", "bnbloc.audit.append_failed",
			"kind", rec.Kind, "name", rec.Name, "action", rec.Action, "error", err)
	}
}
