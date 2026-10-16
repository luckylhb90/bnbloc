package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// AI schedules (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md
// §4): the scheduler claims an ai schedule's slot like any other and hands
// it to the gateway, which carries the task out with the assist model
// (services/gateway/schedule_run.go). Realtime keeps when (the claim, the
// next run, the misfire grace) and records the outcome and the run's
// summary on the schedule. "Run now" with ?dry=1 asks for a dry run: real
// reads, nothing sent, the actions it would take answered to the creator.

// aiPauseAfter is how many failed runs in a row pause an ai schedule: a
// task that keeps failing (no AI, a room it cannot read) stops instead of
// failing every week unnoticed.
const aiPauseAfter = 3

// GatewayAIRuns is the AIRunner over the gateway's internal route.
type GatewayAIRuns struct {
	BaseURL string
	Client  *http.Client
}

func (g GatewayAIRuns) RunSchedule(ctx context.Context, sc wire.RoomSchedule, slot time.Time, dry bool) (wire.ScheduleRunReply, error) {
	var out wire.ScheduleRunReply
	body, err := json.Marshal(wire.ScheduleRunRequest{Schedule: sc, SlotMs: slot.UnixMilli(), DryRun: dry})
	if err != nil {
		return out, err
	}
	req, err := svcauth.NewRequest(ctx, http.MethodPost, strings.TrimRight(g.BaseURL, "/")+"/internal/v1/schedule-runs", bytes.NewReader(body), realtimeServiceSubject, sc.TenantID)
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("gateway answered %d: %s", resp.StatusCode, httpx.ErrorText(raw))
	}
	return out, json.Unmarshal(raw, &out)
}

// runClaimedAtGateway runs a claimed slot of an ai or notice schedule and fills res. A transport failure
// keeps the slot for the next tick (within the misfire grace); a run that
// failed is recorded, and the schedule pauses after aiPauseAfter in a row.
func (s *Service) runClaimedAtGateway(ctx context.Context, sc wire.RoomSchedule, slot time.Time, res *store.ScheduleRunResult) {
	if s.AIRuns == nil {
		res.Status, res.Error, res.Disable, res.NextRunAtMs = wire.ScheduleRunError, gatewayRunsUnavailable(sc.Recipient.Kind)+" are not available on this deployment", true, 0
		return
	}
	reply, err := s.AIRuns.RunSchedule(ctx, sc, slot, false)
	if err != nil {
		res.Status, res.Error, res.NextRunAtMs, res.Disable = wire.ScheduleRunError, "the AI run could not start: "+err.Error(), slot.UnixMilli(), false
		log.Printf("realtime: schedule %s room=%s ai run failed (will retry): %v", sc.ScheduleID, sc.RoomID, err)
		return
	}
	res.Status, res.Error, res.Summary, res.Seq = reply.Status, reply.Error, reply.Summary, reply.Seq
	if reply.Status == wire.ScheduleRunError && sc.FailedRuns+1 >= aiPauseAfter {
		res.Disable, res.NextRunAtMs = true, 0
		res.Error = fmt.Sprintf("%s (paused after %d failed runs)", reply.Error, aiPauseAfter)
	}
}

// runAtGatewayNow is "Run now" of an ai or notice schedule (creator only): ?dry=1 is the dry
// run (nothing recorded), else a real run at now, recorded like a slot.
func (s *Service) runAtGatewayNow(w http.ResponseWriter, r *http.Request, sc wire.RoomSchedule, now time.Time) {
	if s.AIRuns == nil {
		httpx.Unavailable(w, gatewayRunsUnavailable(sc.Recipient.Kind))
		return
	}
	dry := r.URL.Query().Get("dry") == "1"
	reply, err := s.AIRuns.RunSchedule(r.Context(), sc, now, dry)
	if err != nil {
		httpx.WriteStatus(w, http.StatusBadGateway, "the AI run could not start", err)
		return
	}
	if !dry {
		res := store.ScheduleRunResult{ScheduleID: sc.ScheduleID, AtMs: now.UnixMilli(), Status: reply.Status, Error: reply.Error, Summary: reply.Summary, Seq: reply.Seq}
		if err := s.Schedules.CompleteScheduleRun(r.Context(), res); err != nil {
			log.Printf("realtime: schedule %s record run: %v", sc.ScheduleID, err)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, reply)
}
