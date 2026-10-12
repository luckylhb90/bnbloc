package store

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The activity store contract (ActivityStore, AgentUsageReader) over memory
// and MySQL. Concurrent reporters are TestContractConcurrentAppendPublishesOnce.

// A stale or equal session-state write never lowers the stored version.
func TestContractActivityStoreSessionStateIsMonotonic(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-act-"))
		room, session := uniq("room-"), uniq("s-")
		put := func(v int64) int64 {
			t.Helper()
			got, err := b.Activity.PutSessionState(ctx, wire.ReportSessionStateInput{WorkspaceID: room, AgentSessionID: session, MessageVersion: v})
			if err != nil {
				t.Fatalf("PutSessionState(%d): %v", v, err)
			}
			return got
		}
		for _, step := range []struct{ put, want int64 }{{10, 10}, {4, 10}, {10, 10}, {12, 12}, {0, 12}, {-1, 12}} {
			if got := put(step.put); got != step.want {
				t.Fatalf("put %d = %d, want %d", step.put, got, step.want)
			}
		}
		// Another session of the room keeps its own version.
		if got, err := b.Activity.PutSessionState(ctx, wire.ReportSessionStateInput{WorkspaceID: room, AgentSessionID: uniq("s2-"), MessageVersion: 3}); err != nil || got != 3 {
			t.Fatalf("other session = %d, %v", got, err)
		}
	})
}

// AppendMessages returns only the messages newer than the session's
// cursor, once each, and the next cursor: a reporter re-draining from seq 0
// publishes nothing twice, and a gap advances the cursor.
func TestContractActivityStoreAppendReturnsFreshMessagesOnce(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-act-"))
		room, session := uniq("room-"), uniq("s-")
		report := func(seqs ...int64) ([]int64, int64) {
			t.Helper()
			in := wire.ReportMessagesInput{WorkspaceID: room, AgentSessionID: session}
			for _, s := range seqs {
				in.Messages = append(in.Messages, wire.Message{Seq: s, Payload: map[string]any{"role": "assistant", "text": "r"}, CreatedAtUnixMs: s})
			}
			fresh, cursor, err := b.Activity.AppendMessages(ctx, room, in)
			if err != nil {
				t.Fatalf("AppendMessages(%v): %v", seqs, err)
			}
			var got []int64
			for _, m := range fresh {
				got = append(got, m.Seq)
			}
			return got, cursor
		}
		check := func(seqs []int64, wantFresh []int64, wantCursor int64) {
			t.Helper()
			got, cursor := report(seqs...)
			if !equalInts(got, wantFresh) || cursor != wantCursor {
				t.Fatalf("report %v = fresh %v cursor %d; want %v, %d", seqs, got, cursor, wantFresh, wantCursor)
			}
		}
		check(nil, nil, 0)
		check([]int64{1, 2}, []int64{1, 2}, 2)
		check([]int64{1, 2, 3}, []int64{3}, 3) // a re-drain from the start
		check([]int64{5}, []int64{5}, 5)       // a gap
		check([]int64{4}, nil, 5)              // below the cursor: a replay
		check([]int64{7, 6, 6}, []int64{7, 6}, 7)
		// Another session starts from zero.
		session = uniq("s2-")
		check([]int64{1}, []int64{1}, 1)
	})
}

// AgentUsage groups reported replies per (owner, provider), in that order,
// counts replies created at/after sinceMs, and is [] when nothing matches.
func TestContractAgentUsageReaderGroupsPerOwnerAndProvider(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-usage-"))
		usage := b.AgentUsage()
		if got := must(usage.AgentUsage(ctx, "", 0))(t); got == nil || len(got) != 0 {
			t.Fatalf("no replies = %#v, want []", got)
		}
		room := uniq("room-")
		reply := func(seq int64, owner, provider string, at int64) wire.Message {
			return wire.Message{Seq: seq, CreatedAtUnixMs: at, Payload: map[string]any{"role": "assistant", "text": "ok", "ownerUserId": owner, "provider": provider,
				"metrics": map[string]any{"inputTokens": 10.0, "outputTokens": 1.0, "costUsd": 0.5}}}
		}
		in := wire.ReportMessagesInput{WorkspaceID: room, AgentSessionID: uniq("s-"), Messages: []wire.Message{
			reply(1, "maya", "zeta", 2000),
			reply(2, "jack", "codex", 2000),
			reply(3, "maya", "codex", 1000), // exactly at sinceMs: counted
			reply(4, "maya", "codex", 999),  // before it
			reply(5, "maya", "codex", 3000),
		}}
		if _, _, err := b.Activity.AppendMessages(ctx, room, in); err != nil {
			t.Fatal(err)
		}
		got := must(usage.AgentUsage(ctx, "", 1000))(t)
		var rows []string
		for _, u := range got {
			rows = append(rows, u.OwnerUserID+"/"+u.Provider)
		}
		if !equalStrings(rows, []string{"jack/codex", "maya/codex", "maya/zeta"}) {
			t.Fatalf("rows = %v", rows)
		}
		if mc := got[1]; mc.ReportedTurns != 2 || mc.InputTokens != 20 || mc.OutputTokens != 2 || mc.CostUsd != 1 || mc.CostReportedBy != 2 {
			t.Fatalf("maya/codex = %+v", mc)
		}
	})
}
