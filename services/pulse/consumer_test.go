package pulsesvc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/luckylhb90/bnbloc/internal/pulse"
)

var uniqueN int

// uniq returns a name no other test uses (streams and tenants are shared in the test Redis).
func uniq(prefix string) string {
	uniqueN++
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), uniqueN)
}

// emitAll writes events through a real emitter and the Redis sink, and waits for
// the batcher to flush them.
func emitAll(t *testing.T, sinkStream string, events ...pulse.Event) {
	t.Helper()
	rdb := testRedis(t)
	em := pulse.New(pulse.NewRedisSink(rdb, sinkStream, 100000), pulse.Config{FlushInterval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()
	for _, ev := range events {
		if !em.Emit(ev) {
			t.Fatalf("emit refused %s", ev.Kind)
		}
	}
	waitFor(t, "emitter flush", func() bool { return em.Stats().Flushed == int64(len(events)) })
	cancel()
	<-done
}

// runUntil starts a consumer and returns a stop function that waits for its final flush.
func runUntil(t *testing.T, c *Consumer) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

func sampleEvents(tnt string, at int64, member, job string) []pulse.Event {
	cost := 0.5
	return []pulse.Event{
		pulse.Event{ID: "claim:" + job, Tenant: tnt, Kind: pulse.KindJobClaimed, At: at}.With(pulse.Enum("core", "codex")).With(pulse.Hash("job", pulse.Digest(job))),
		pulse.Event{ID: "seen:" + member, Tenant: tnt, Kind: pulse.KindPresenceSeen, At: at}.With(pulse.Hash("member", member)),
		pulse.Event{ID: "msg:1", Tenant: tnt, Kind: pulse.KindMessageSent, At: at}.With(pulse.Enum("channelType", "agent")).With(pulse.Hash("member", member)),
		pulse.Event{ID: "reply:1", Tenant: tnt, Kind: pulse.KindReplyUsage, At: at}.With(pulse.Enum("core", "codex")).With(pulse.Flag("reported", true)).With(pulse.Num("tokensIn", 100)).With(pulse.Num("tokensOut", 20)).With(pulse.Num("costUsd", cost)),
		pulse.Event{ID: "reply:2", Tenant: tnt, Kind: pulse.KindReplyUsage, At: at}.With(pulse.Enum("core", "codex")).With(pulse.Flag("reported", false)),
		pulse.Event{ID: "fin:" + job, Tenant: tnt, Kind: pulse.KindJobFinished, At: at}.With(pulse.Enum("core", "codex")).With(pulse.Enum("status", "replied")).With(pulse.Hash("job", pulse.Digest(job))),
		pulse.Event{ID: "art:1", Tenant: tnt, Kind: pulse.KindArtifactVersion, At: at}.With(pulse.Ident("kind", "deck")).With(pulse.Flag("isNew", true)),
		pulse.Event{ID: "app:1", Tenant: tnt, Kind: pulse.KindAppWritten, At: at}.With(pulse.Flag("public", true)),
	}
}

// The end-to-end count: every event is counted exactly once, the duplicate and
// the garbage entry are dropped, the live state is right, and every message is
// acked once the rollup has committed.
func TestConsumerCountsEachEventOnceAndAcksAfterTheCommit(t *testing.T) {
	rdb := testRedis(t)
	tnt, stream := uniq("tenant"), uniq("pulse:test")
	at := time.Now().UnixMilli()
	member := pulse.Digest("member-" + tnt)
	evs := sampleEvents(tnt, at, member, "job-"+tnt)
	emitAll(t, stream, evs...)

	// A redelivered duplicate and an entry the schema refuses.
	raw, err := evs[0].MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"e": string(raw)}})
	rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"e": "{not json"}})

	rows := newFakeRows()
	c := NewConsumer(rdb, rows, ConsumerConfig{Stream: stream, Group: "g", Name: "c1", FlushEvery: 10 * time.Millisecond, PruneEvery: time.Hour, Retention: time.Hour})
	stop := runUntil(t, c)
	waitFor(t, "rollup", func() bool { return rows.total(tnt, MetricReplies) == 2 })
	waitFor(t, "duplicate and invalid counted", func() bool {
		return c.Stats().Duplicates.Load() == 1 && c.Stats().Invalid.Load() == 1
	})
	stop()

	check := func(metric string, want float64) {
		t.Helper()
		if got := rows.total(tnt, metric); got != want {
			t.Errorf("%s = %v, want %v", metric, got, want)
		}
	}
	check(MetricReplies, 2)
	check(MetricRepliesReported, 1)
	check(MetricTokensIn, 100)
	check(MetricTokensOut, 20)
	check(MetricCostUsd, 0.5)
	check(MetricJobsClaimed, 1)
	check(MetricJobsFinished, 1)
	check(MetricMessages, 1)
	check(MetricArtifactVersions, 1)
	check(MetricAppWrites, 1)

	state := NewLive(rdb)
	now := time.Now()
	if n, _ := state.PeopleOnline(ctx, tnt, now); n != 1 {
		t.Errorf("people online = %d, want 1", n)
	}
	if n, _ := state.AgentsWorking(ctx, tnt, now); n != 0 {
		t.Errorf("agents working = %d, want 0 (claimed, then finished)", n)
	}
	if n, _ := state.ActivePeopleWeek(ctx, tnt, now); n != 1 {
		t.Errorf("active people this week = %d, want 1", n)
	}
	if p, _ := rdb.XPending(ctx, stream, "g").Result(); p.Count != 0 {
		t.Errorf("%d messages still pending after the commit", p.Count)
	}
}

// A failed rollup write keeps its deltas and its messages pending. The retry
// adds the deltas once: no loss, no double count.
func TestConsumerRetriesAFailedFlushWithoutDoubleCounting(t *testing.T) {
	rdb := testRedis(t)
	tnt, stream := uniq("tenant"), uniq("pulse:test")
	at := time.Now().UnixMilli()
	emitAll(t, stream, sampleEvents(tnt, at, pulse.Digest("m-"+tnt), "job-"+tnt)...)

	rows := newFakeRows()
	rows.failNext = 1
	c := NewConsumer(rdb, rows, ConsumerConfig{Stream: stream, Group: "g", Name: "c1", FlushEvery: 10 * time.Millisecond, PruneEvery: time.Hour, Retention: time.Hour})
	stop := runUntil(t, c)
	waitFor(t, "a failed flush", func() bool { return c.Stats().FlushErrors.Load() >= 1 })
	waitFor(t, "the retry", func() bool { return c.Stats().Flushed.Load() >= 1 })
	stop()
	if got := rows.total(tnt, MetricReplies); got != 2 {
		t.Fatalf("replies after retry = %v, want exactly 2", got)
	}
}

// A consumer that dies after reading and before flushing loses its in-memory
// deltas, and its messages stay pending. The next run recovers them and counts
// each one once.
func TestConsumerRecoversUnackedMessagesAfterACrash(t *testing.T) {
	rdb := testRedis(t)
	tnt, stream := uniq("tenant"), uniq("pulse:test")
	ctx := context.Background()
	at := time.Now().UnixMilli()
	if err := rdb.XGroupCreateMkStream(ctx, stream, "g", "0").Err(); err != nil {
		t.Fatal(err)
	}
	emitAll(t, stream,
		pulse.Event{ID: "r:1", Tenant: tnt, Kind: pulse.KindReplyUsage, At: at}.With(pulse.Enum("core", "codex")).With(pulse.Flag("reported", false)),
		pulse.Event{ID: "r:2", Tenant: tnt, Kind: pulse.KindReplyUsage, At: at}.With(pulse.Enum("core", "codex")).With(pulse.Flag("reported", false)),
		pulse.Event{ID: "r:3", Tenant: tnt, Kind: pulse.KindReplyUsage, At: at}.With(pulse.Enum("core", "codex")).With(pulse.Flag("reported", false)),
	)

	rows := newFakeRows()
	crashed := NewConsumer(rdb, rows, ConsumerConfig{Stream: stream, Group: "g", Name: "same", FlushEvery: time.Hour, PruneEvery: time.Hour, Retention: time.Hour})
	crashed.readBatch(ctx, ">") // reads three messages, then the process dies: no flush, no ack
	if got := rows.total(tnt, MetricReplies); got != 0 {
		t.Fatalf("nothing should be committed before the crash, got %v", got)
	}

	c := NewConsumer(rdb, rows, ConsumerConfig{Stream: stream, Group: "g", Name: "same", FlushEvery: 10 * time.Millisecond, PruneEvery: time.Hour, Retention: time.Hour})
	stop := runUntil(t, c)
	waitFor(t, "recovered rollup", func() bool { return rows.total(tnt, MetricReplies) == 3 })
	stop()
	if got := rows.total(tnt, MetricReplies); got != 3 {
		t.Fatalf("replies after recovery = %v, want exactly 3", got)
	}
	if p, _ := rdb.XPending(ctx, stream, "g").Result(); p.Count != 0 {
		t.Fatalf("%d messages still pending after recovery", p.Count)
	}
}

// Waiting jobs: queued until claimed, then working until finished. The weekly
// active set counts a deliverable's author as well as a message sender.
func TestLiveStateTracksWaitingWorkingAndActivePeople(t *testing.T) {
	rdb := testRedis(t)
	tnt := uniq("tenant")
	ctx := context.Background()
	now := time.Now()
	state := NewLive(rdb)
	job := pulse.Digest("job-" + tnt)
	member := pulse.Digest("author-" + tnt)
	ms := now.UnixMilli()

	pipe := rdb.Pipeline()
	state.Apply(pipe, ctx, pulse.Event{ID: "q", Tenant: tnt, Kind: pulse.KindJobQueued, At: ms}.With(pulse.Enum("core", "codex")).With(pulse.Hash("job", job)))
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := state.AgentsWaiting(ctx, tnt, now); n != 1 {
		t.Fatalf("waiting after queue = %d, want 1", n)
	}
	if _, has, _ := state.OldestWaiting(ctx, tnt, now); !has {
		t.Fatal("the oldest waiting job must be reported")
	}

	pipe = rdb.Pipeline()
	state.Apply(pipe, ctx, pulse.Event{ID: "c", Tenant: tnt, Kind: pulse.KindJobClaimed, At: ms}.With(pulse.Enum("core", "codex")).With(pulse.Hash("job", job)))
	state.Apply(pipe, ctx, pulse.Event{ID: "a", Tenant: tnt, Kind: pulse.KindArtifactVersion, At: ms}.With(pulse.Ident("kind", "deck")).With(pulse.Flag("isNew", true)).With(pulse.Hash("member", member)))
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := state.AgentsWaiting(ctx, tnt, now); n != 0 {
		t.Fatalf("waiting after claim = %d, want 0", n)
	}
	if n, _ := state.AgentsWorking(ctx, tnt, now); n != 1 {
		t.Fatalf("working after claim = %d, want 1", n)
	}
	if n, _ := state.ActivePeopleWeek(ctx, tnt, now); n != 1 {
		t.Fatalf("active people = %d, want 1 (the deliverable's author)", n)
	}

	pipe = rdb.Pipeline()
	state.Apply(pipe, ctx, pulse.Event{ID: "f", Tenant: tnt, Kind: pulse.KindJobFinished, At: ms}.With(pulse.Enum("core", "codex")).With(pulse.Enum("status", "replied")).With(pulse.Hash("job", job)))
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := state.AgentsWorking(ctx, tnt, now); n != 0 {
		t.Fatalf("working after finish = %d, want 0", n)
	}
}

// Member locations: a member is placed in the country of their zone, moves when
// the zone changes, leaves with presence, and a zone the table does not know
// places nobody.
func TestLiveStateCountsMembersByCountry(t *testing.T) {
	rdb := testRedis(t)
	tnt := uniq("tenant")
	ctx := context.Background()
	now := time.Now()
	state := NewLive(rdb)
	ms := now.UnixMilli()
	member := pulse.Digest("geo-" + tnt)
	seen := func(country string) {
		pipe := rdb.Pipeline()
		ev := pulse.Event{ID: "s" + country, Tenant: tnt, Kind: pulse.KindPresenceSeen, At: ms}.With(pulse.Hash("member", member))
		if country != "" {
			ev = ev.With(pulse.Ident("country", country))
		}
		state.Apply(pipe, ctx, ev)
		if _, err := pipe.Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	countryCount := func(cc string) int64 {
		cs, err := state.Countries(ctx, tnt, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			if c.Code == cc {
				return c.People
			}
		}
		return 0
	}
	seen("de")
	if countryCount("DE") != 1 {
		t.Fatalf("a member in Berlin counts in DE once, got %d", countryCount("DE"))
	}
	seen("fr")
	if countryCount("DE") != 0 || countryCount("FR") != 1 {
		t.Fatalf("a member who moved counts only in FR: DE %d, FR %d", countryCount("DE"), countryCount("FR"))
	}
	seen("")
	if countryCount("FR") != 0 {
		t.Fatal("an unknown zone places the member in no country")
	}
	seen("de")
	pipe := rdb.Pipeline()
	state.Apply(pipe, ctx, pulse.Event{ID: "l", Tenant: tnt, Kind: pulse.KindPresenceLeft, At: ms}.With(pulse.Hash("member", member)).With(pulse.Ident("country", "de")))
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if countryCount("DE") != 0 {
		t.Fatal("a member who left is in no country")
	}
}
