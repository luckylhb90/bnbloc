package pulse

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func jobEvent(i int) Event {
	return Event{ID: "e" + itoa(i), Tenant: "t1", Kind: KindJobClaimed, At: 1}.With(Enum("core", "codex"))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// A nil emitter is the disabled emitter (G6): every call is a no-op.
func TestNilEmitterIsDisabled(t *testing.T) {
	var e *Emitter
	if e.Emit(jobEvent(1)) {
		t.Fatal("a disabled emitter must not accept")
	}
	e.Run(context.Background()) // returns at once
	select {
	case <-e.Done():
	default:
		t.Fatal("Done of a disabled emitter must be closed")
	}
	if e.Stats() != (Stats{}) || e.Queued() != 0 {
		t.Fatal("a disabled emitter reports zeros")
	}
}

func TestNewWithoutSinkIsDisabled(t *testing.T) {
	if New(nil, Config{}) != nil {
		t.Fatal("no sink means the disabled emitter (no goroutine, no queue)")
	}
}

// A sink that stalls (ignoring its context until the timeout) must not slow the
// product's Emit (P1, gate G8). The queue fills and the excess is counted.
func TestEmitNeverBlocksOnAStalledSink(t *testing.T) {
	stall := SinkFunc(func(ctx context.Context, _ []Event) error {
		<-ctx.Done()
		return ctx.Err()
	})
	e := New(stall, Config{QueueSize: 64, BatchSize: 8, WriteTimeout: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)

	start := time.Now()
	const n = 50000
	accepted := 0
	for i := 0; i < n; i++ {
		if e.Emit(jobEvent(i)) {
			accepted++
		}
	}
	elapsed := time.Since(start)
	cancel()
	<-e.Done()

	if elapsed > 2*time.Second {
		t.Fatalf("%d emits took %s with a stalled sink: Emit is blocking", n, elapsed)
	}
	st := e.Stats()
	if st.DroppedQueue == 0 || accepted == n {
		t.Fatalf("with a full queue the excess must be dropped and counted: %+v", st)
	}
	if int64(accepted)+st.DroppedQueue != n {
		t.Fatalf("every emit is accepted or counted as dropped: accepted %d, stats %+v", accepted, st)
	}
}

func TestBatchesBySizeAndByInterval(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	sink := SinkFunc(func(_ context.Context, batch []Event) error {
		mu.Lock()
		sizes = append(sizes, len(batch))
		mu.Unlock()
		return nil
	})
	e := New(sink, Config{QueueSize: 100, BatchSize: 4, FlushInterval: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	for i := 0; i < 8; i++ {
		e.Emit(jobEvent(i))
	}
	e.Emit(jobEvent(99)) // a partial batch: written by the interval
	waitUntil(t, func() bool { return e.Stats().Flushed == 9 })
	cancel()
	<-e.Done()

	mu.Lock()
	defer mu.Unlock()
	if len(sizes) < 3 || sizes[0] != 4 || sizes[1] != 4 {
		t.Fatalf("batch sizes = %v, want two full batches of 4 then the interval's partial batch", sizes)
	}
}

// Shutdown writes what was queued (best effort), so a clean stop loses nothing.
func TestShutdownFlushesWhatIsQueued(t *testing.T) {
	var got atomic.Int64
	sink := SinkFunc(func(_ context.Context, batch []Event) error {
		got.Add(int64(len(batch)))
		return nil
	})
	e := New(sink, Config{QueueSize: 100, BatchSize: 1000, FlushInterval: time.Hour})
	for i := 0; i < 30; i++ {
		e.Emit(jobEvent(i))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.Run(ctx) // already cancelled: drains and returns
	if got.Load() != 30 {
		t.Fatalf("shutdown wrote %d of 30 queued events", got.Load())
	}
}

// Consecutive failed writes open the breaker: later batches are dropped without
// a write, so a dead sink costs one timeout per cooldown, not one per batch.
func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	var calls atomic.Int64
	sink := SinkFunc(func(context.Context, []Event) error {
		calls.Add(1)
		return errors.New("backend down")
	})
	e := New(sink, Config{QueueSize: 100, BatchSize: 1, FlushInterval: time.Hour,
		BreakerThreshold: 2, BreakerCooldown: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	for i := 0; i < 10; i++ {
		e.Emit(jobEvent(i))
	}
	waitUntil(t, func() bool { st := e.Stats(); return st.DroppedBreaker+st.SinkErrors == 10 })
	cancel()
	<-e.Done()

	st := e.Stats()
	if calls.Load() != 2 || st.SinkErrors != 2 || st.DroppedBreaker != 8 {
		t.Fatalf("calls %d, stats %+v: the breaker must stop writes after two failures", calls.Load(), st)
	}
}

func TestBreakerRecoversAfterCooldown(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	sink := SinkFunc(func(context.Context, []Event) error {
		if fail.Load() {
			return errors.New("down")
		}
		return nil
	})
	e := New(sink, Config{QueueSize: 100, BatchSize: 1, FlushInterval: time.Hour,
		BreakerThreshold: 1, BreakerCooldown: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	e.Emit(jobEvent(1)) // fails: breaker opens
	waitUntil(t, func() bool { return e.Stats().SinkErrors == 1 })
	fail.Store(false)
	time.Sleep(60 * time.Millisecond) // past the cooldown
	e.Emit(jobEvent(2))
	waitUntil(t, func() bool { return e.Stats().Flushed == 1 })
	cancel()
	<-e.Done()
}

// Invalid events are refused and counted by reason; none reaches the queue.
func TestInvalidEventsNeverReachTheQueue(t *testing.T) {
	e := New(Discard, Config{QueueSize: 10})
	if e.Emit(Event{ID: "x", Tenant: "t", Kind: "not.a.kind"}) {
		t.Fatal("unknown kind accepted")
	}
	if e.Queued() != 0 || e.Stats().Invalid != 1 {
		t.Fatalf("queued %d, stats %+v", e.Queued(), e.Stats())
	}
}

// The enqueue path allocates nothing (G1): an event is a value, the queue holds
// values, and validation copies into the same fixed array.
func TestEmitAllocatesNothing(t *testing.T) {
	e := New(Discard, Config{QueueSize: 4096})
	ev := jobEvent(1)
	n := testing.AllocsPerRun(1000, func() { e.Emit(ev) })
	if n != 0 {
		t.Fatalf("Emit allocates %v per call, want 0", n)
	}
}

func BenchmarkEmit(b *testing.B) {
	e := New(Discard, Config{QueueSize: 1 << 16})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	ev := jobEvent(1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Emit(ev)
	}
}

// BenchmarkEmitDisabled is the cost a deployment without Pulse pays per call.
func BenchmarkEmitDisabled(b *testing.B) {
	var e *Emitter
	ev := jobEvent(1)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		e.Emit(ev)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// A sink that ignores its context and never returns (a hung socket) holds the
// batcher, not the product: Emit keeps returning at once, and the excess is
// counted (P1 at its worst case).
func TestEmitNeverBlocksOnAHungSink(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hung := SinkFunc(func(context.Context, []Event) error {
		<-release
		return nil
	})
	e := New(hung, Config{QueueSize: 32, BatchSize: 4, WriteTimeout: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	start := time.Now()
	for i := 0; i < 20000; i++ {
		e.Emit(jobEvent(i))
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("20000 emits took %s with a hung sink: Emit is blocking", elapsed)
	}
	if e.Stats().DroppedQueue == 0 {
		t.Fatal("a hung sink must fill the queue and be counted")
	}
}
