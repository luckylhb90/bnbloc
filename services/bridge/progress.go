package bridge

import (
	"context"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A running turn's progress (docs/prd/2026-10-13-live-turns.md §3): reflow
// relays the core's turn activity (TurnStatus.StartedAtMs, Activity) to the
// room as agent_job_progress, through activity (POST
// /v1/bridge/jobs/{jobId}/progress). Transient and best-effort: a failed
// relay never touches the turn.

// progressEvery bounds how often one turn's progress is relayed.
const progressEvery = 2 * time.Second

// progressRelay is what one turn last relayed, and when.
type progressRelay struct {
	last   wire.JobProgressInput
	sentAt time.Time
}

// relayProgress reports ts's activity for job when it changed and the
// last report is progressEvery old.
func (b *Bridge) relayProgress(ctx context.Context, jobID string, ts corepkg.TurnStatus, p *progressRelay) {
	if jobID == "" || (ts.StartedAtMs == 0 && len(ts.Activity) == 0) {
		return
	}
	in := wire.JobProgressInput{BridgeID: b.bridgeID(), StartedAtMs: ts.StartedAtMs, Activity: jobActivity(ts.Activity)}
	if reflect.DeepEqual(in, p.last) || time.Since(p.sentAt) < progressEvery {
		return
	}
	p.last, p.sentAt = in, time.Now()
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := b.gatewayPOST(rctx, "/v1/bridge/jobs/"+url.PathEscape(jobID)+"/progress", in); err != nil {
		slog.Debug("bnbloc bridge progress relay failed", "event", "bnbloc.bridge.progress_failed", "job", jobID, "error", err)
	}
}

// jobActivity maps a core's turn activity onto the wire, held to its
// bounds (the newest MaxJobActivity steps, titles cut).
func jobActivity(in []corepkg.TurnActivity) []wire.JobActivity {
	if len(in) > wire.MaxJobActivity {
		in = in[len(in)-wire.MaxJobActivity:]
	}
	out := make([]wire.JobActivity, 0, len(in))
	for _, a := range in {
		title := strings.TrimSpace(a.Title)
		if utf8.RuneCountInString(title) > wire.MaxJobActivityTitle {
			title = string([]rune(title)[:wire.MaxJobActivityTitle-1]) + "…"
		}
		out = append(out, wire.JobActivity{Kind: a.Kind, Title: title, AtMs: a.AtMs})
	}
	return out
}
