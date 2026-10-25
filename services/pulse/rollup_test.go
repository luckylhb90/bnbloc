package pulsesvc

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
)

const at = int64(1761300000000) // a whole minute, UTC

func reply(core string, reported bool, in, out float64, cost *float64) pulse.Event {
	ev := pulse.Event{ID: "r", Tenant: "t1", Kind: pulse.KindReplyUsage, At: at}.With(pulse.Enum("core", core)).With(pulse.Flag("reported", reported))
	if reported {
		ev = ev.With(pulse.Num("tokensIn", in)).With(pulse.Num("tokensOut", out))
		if cost != nil {
			ev = ev.With(pulse.Num("costUsd", *cost))
		}
	}
	return ev
}

func sumOf(rows []store.PulseMinuteDelta, metric string) float64 {
	var s float64
	for _, r := range rows {
		if r.Metric == metric {
			s += r.Value
		}
	}
	return s
}

func TestRollupCountsReplyUsageAndCoverage(t *testing.T) {
	r := NewRollup()
	cost := 0.5
	r.Add(reply("codex", true, 100, 20, &cost))
	r.Add(reply("codex", false, 0, 0, nil)) // a reply that reported no usage
	rows := drain(r)
	if got := sumOf(rows, MetricReplies); got != 2 {
		t.Fatalf("replies = %v, want 2", got)
	}
	if got := sumOf(rows, MetricRepliesReported); got != 1 {
		t.Fatalf("replies_reported = %v, want 1", got)
	}
	if sumOf(rows, MetricTokensIn) != 100 || sumOf(rows, MetricTokensOut) != 20 || sumOf(rows, MetricCostUsd) != 0.5 {
		t.Fatalf("usage sums wrong: %+v", rows)
	}
}

func TestRollupBucketsByMinuteAndDimension(t *testing.T) {
	r := NewRollup()
	r.Add(pulse.Event{ID: "j1", Tenant: "t1", Kind: pulse.KindJobFinished, At: at + 30_000}.With(pulse.Enum("core", "codex")).With(pulse.Enum("status", "replied")))
	r.Add(pulse.Event{ID: "j2", Tenant: "t1", Kind: pulse.KindJobFinished, At: at + 59_999}.With(pulse.Enum("core", "codex")).With(pulse.Enum("status", "failed")))
	r.Add(pulse.Event{ID: "j3", Tenant: "t1", Kind: pulse.KindJobFinished, At: at + 60_000}.With(pulse.Enum("core", "codex")).With(pulse.Enum("status", "replied")))
	rows := drain(r)
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3 (two finished in minute one, one in minute two)", len(rows))
	}
	for _, row := range rows {
		if row.MinuteMs%60_000 != 0 {
			t.Fatalf("minute %d is not a whole minute", row.MinuteMs)
		}
	}
}

func TestRollupIgnoresKindsItDoesNotCount(t *testing.T) {
	r := NewRollup()
	r.Add(pulse.Event{ID: "p", Tenant: "t1", Kind: pulse.KindPresenceSeen, At: at}.With(pulse.Hash("member", pulse.Digest("m"))))
	if r.Len() != 0 {
		t.Fatalf("presence is live state, not a rollup row: %d rows", r.Len())
	}
}

// A failed write hands its deltas back, so the retry adds them once.
func TestRollupRestoreKeepsDeltasForRetry(t *testing.T) {
	r := NewRollup()
	r.Add(reply("codex", true, 5, 5, nil))
	rows := drain(r)
	r.Restore(rows)
	again := drain(r)
	if sumOf(again, MetricReplies) != 1 {
		t.Fatalf("restored replies = %v, want 1", sumOf(again, MetricReplies))
	}
}

func drain(r *Rollup) []store.PulseMinuteDelta { return r.Drain() }
