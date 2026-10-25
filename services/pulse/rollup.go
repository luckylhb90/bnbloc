// Package pulsesvc is the organization board's backend: it consumes the Pulse
// stream, keeps the live state and the minute rollups, and answers the board's
// snapshot (docs/prd/2026-10-24-executive-pulse-backend.md, sections 2 to 5).
package pulsesvc

import (
	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Metric names of the rollup (the catalogue the board reads). A name is part of
// the contract: rename one only with the board.
const (
	MetricJobsClaimed      = "jobs_claimed"
	MetricJobsFinished     = "jobs_finished"
	MetricJobsSwept        = "jobs_swept"
	MetricReplies          = "replies"
	MetricRepliesReported  = "replies_reported"
	MetricTokensIn         = "tokens_in"
	MetricTokensOut        = "tokens_out"
	MetricCostUsd          = "cost_usd"
	MetricMessages         = "messages"
	MetricArtifactVersions = "artifact_versions"
	MetricAppWrites        = "app_writes"
)

// minuteMs is the rollup's bucket.
const minuteMs = 60_000

type rkey struct {
	tenant string
	minute int64
	metric string
	dim    string
}

// Rollup accumulates one flush interval's minute deltas in memory. Only the
// consumer touches it, so it has no lock.
type Rollup struct {
	sums map[rkey]float64
}

// NewRollup returns an empty rollup.
func NewRollup() *Rollup { return &Rollup{sums: map[rkey]float64{}} }

// Add folds one event into the rollup. An event kind the rollup does not count
// is ignored (live state may still use it).
func (r *Rollup) Add(ev pulse.Event) {
	minute := ev.At - ev.At%minuteMs
	inc := func(metric, dim string, v float64) {
		r.sums[rkey{tenant: ev.Tenant, minute: minute, metric: metric, dim: dim}] += v
	}
	switch ev.Kind {
	case pulse.KindJobClaimed:
		inc(MetricJobsClaimed, text(ev, "core"), 1)
	case pulse.KindJobFinished:
		inc(MetricJobsFinished, text(ev, "core")+"/"+text(ev, "status"), 1)
	case pulse.KindJobSwept:
		inc(MetricJobsSwept, text(ev, "reason"), 1)
	case pulse.KindReplyUsage:
		core := text(ev, "core")
		inc(MetricReplies, core, 1)
		if flag(ev, "reported") {
			inc(MetricRepliesReported, core, 1)
			if v, ok := number(ev, "tokensIn"); ok {
				inc(MetricTokensIn, core, v)
			}
			if v, ok := number(ev, "tokensOut"); ok {
				inc(MetricTokensOut, core, v)
			}
			if v, ok := number(ev, "costUsd"); ok {
				inc(MetricCostUsd, core, v)
			}
		}
	case pulse.KindMessageSent:
		inc(MetricMessages, text(ev, "channelType"), 1)
	case pulse.KindArtifactVersion:
		dim := "update"
		if flag(ev, "isNew") {
			dim = "new"
		}
		inc(MetricArtifactVersions, dim, 1)
	case pulse.KindAppWritten:
		dim := "private"
		if flag(ev, "public") {
			dim = "public"
		}
		inc(MetricAppWrites, dim, 1)
	}
}

// Len reports how many rows the next Drain would write.
func (r *Rollup) Len() int { return len(r.sums) }

// Drain returns the deltas accumulated so far and clears the rollup.
func (r *Rollup) Drain() []store.PulseMinuteDelta {
	rows := make([]store.PulseMinuteDelta, 0, len(r.sums))
	for k, v := range r.sums {
		rows = append(rows, store.PulseMinuteDelta{Tenant: k.tenant, MinuteMs: k.minute, Metric: k.metric, Dim: k.dim, Value: v})
	}
	r.sums = map[rkey]float64{}
	return rows
}

// Restore adds rows back after a failed write, so the next flush retries them.
func (r *Rollup) Restore(rows []store.PulseMinuteDelta) {
	for _, row := range rows {
		r.sums[rkey{tenant: row.Tenant, minute: row.MinuteMs, metric: row.Metric, dim: row.Dim}] += row.Value
	}
}

func text(ev pulse.Event, key string) string {
	if a, ok := ev.Get(key); ok {
		return a.Text()
	}
	return ""
}

func flag(ev pulse.Event, key string) bool {
	if a, ok := ev.Get(key); ok {
		return a.Bool()
	}
	return false
}

func number(ev pulse.Event, key string) (float64, bool) {
	if a, ok := ev.Get(key); ok {
		return a.Number(), true
	}
	return 0, false
}
