package pulse

import (
	"context"
	"sync/atomic"
	"time"
)

// Sink receives batches of checked events. A Sink may be slow or fail: the
// emitter absorbs both without the product noticing (see Emit and flush).
type Sink interface {
	Write(ctx context.Context, batch []Event) error
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(ctx context.Context, batch []Event) error

// Write calls f.
func (f SinkFunc) Write(ctx context.Context, batch []Event) error { return f(ctx, batch) }

// Config bounds the emitter. Zero fields take the defaults below.
type Config struct {
	QueueSize        int           // events waiting between product and batcher
	BatchSize        int           // events per Sink.Write
	FlushInterval    time.Duration // a partial batch is written after this long
	WriteTimeout     time.Duration // one Sink.Write gets this long
	BreakerThreshold int           // consecutive failed writes that open the breaker
	BreakerCooldown  time.Duration // how long an open breaker drops batches
}

// Defaults (docs/prd/2026-10-24-executive-pulse-backend.md, section 5).
const (
	defaultQueueSize        = 8192
	defaultBatchSize        = 256
	defaultFlushInterval    = 100 * time.Millisecond
	defaultWriteTimeout     = 2 * time.Second
	defaultBreakerThreshold = 3
	defaultBreakerCooldown  = 30 * time.Second
)

// Stats are the emitter's counters. They only grow.
type Stats struct {
	Accepted       int64 // queued for the sink
	Flushed        int64 // written by the sink
	Invalid        int64 // refused by the schema (unknown kind, bad tenant, ...)
	AttrDropped    int64 // attributes removed by the schema, events still accepted
	DroppedQueue   int64 // queue full: the product's enqueue was dropped
	DroppedSink    int64 // a write failed: its batch was dropped
	DroppedBreaker int64 // the breaker was open: batches were dropped without a write
	SinkErrors     int64 // failed writes
}

// Emitter is the bounded, non-blocking front of one replica's events. A nil
// *Emitter is the disabled emitter: every method is a no-op, and no goroutine
// or connection exists.
type Emitter struct {
	cfg   Config
	sink  Sink
	queue chan Event
	done  chan struct{}

	accepted, flushed, attrDropped        atomic.Int64
	droppedQueue, droppedSink, droppedBrk atomic.Int64
	sinkErrors                            atomic.Int64
	invalid                               [numReasons]atomic.Int64

	// Breaker state belongs to the Run goroutine alone; no lock.
	failures  int
	openUntil time.Time
}

// New returns an emitter writing to sink, or nil (disabled) when sink is nil.
// Call Run once to start the batcher.
func New(sink Sink, cfg Config) *Emitter {
	if sink == nil {
		return nil
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultFlushInterval
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.BreakerThreshold <= 0 {
		cfg.BreakerThreshold = defaultBreakerThreshold
	}
	if cfg.BreakerCooldown <= 0 {
		cfg.BreakerCooldown = defaultBreakerCooldown
	}
	return &Emitter{
		cfg:   cfg,
		sink:  sink,
		queue: make(chan Event, cfg.QueueSize),
		done:  make(chan struct{}),
	}
}

// Emit checks the event and queues it without blocking. It reports whether the
// event was queued. A product request calls Emit after its own commit and
// ignores the result: a refused or dropped event costs a counter, never time.
func (e *Emitter) Emit(ev Event) bool {
	if e == nil {
		return false
	}
	out, dropped, r := check(ev)
	if dropped > 0 {
		e.attrDropped.Add(int64(dropped))
	}
	if r != reasonOK {
		e.invalid[r].Add(1)
		return false
	}
	select {
	case e.queue <- out:
		e.accepted.Add(1)
		return true
	default:
		e.droppedQueue.Add(1)
		return false
	}
}

// Run is the batcher. It returns when ctx is cancelled, after writing what is
// queued (best effort: a breaker that is open drops it). Done is closed then.
func (e *Emitter) Run(ctx context.Context) {
	if e == nil {
		return
	}
	defer close(e.done)
	ticker := time.NewTicker(e.cfg.FlushInterval)
	defer ticker.Stop()
	batch := make([]Event, 0, e.cfg.BatchSize)
	for {
		select {
		case <-ctx.Done():
			batch = e.drain(batch)
			e.flush(batch)
			return
		case ev := <-e.queue:
			batch = append(batch, ev)
			if len(batch) >= e.cfg.BatchSize {
				e.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				e.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// Done is closed when Run has returned.
func (e *Emitter) Done() <-chan struct{} {
	if e == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return e.done
}

// drain takes what is queued at the moment of shutdown, in batches.
func (e *Emitter) drain(batch []Event) []Event {
	for {
		select {
		case ev := <-e.queue:
			batch = append(batch, ev)
			if len(batch) >= e.cfg.BatchSize {
				e.flush(batch)
				batch = batch[:0]
			}
		default:
			return batch
		}
	}
}

// flush writes one batch. A failed write drops the batch (no retry queue: the
// board's numbers are reconciled elsewhere, and an unbounded retry queue would
// be memory the product pays for). Consecutive failures open the breaker, so a
// dead sink costs one timeout per cooldown instead of one per batch.
func (e *Emitter) flush(batch []Event) {
	if len(batch) == 0 {
		return
	}
	now := time.Now()
	if now.Before(e.openUntil) {
		e.droppedBrk.Add(int64(len(batch)))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.WriteTimeout)
	err := e.sink.Write(ctx, batch)
	cancel()
	if err != nil {
		e.sinkErrors.Add(1)
		e.droppedSink.Add(int64(len(batch)))
		e.failures++
		if e.failures >= e.cfg.BreakerThreshold {
			e.openUntil = now.Add(e.cfg.BreakerCooldown)
			e.failures = 0
		}
		return
	}
	e.failures = 0
	e.flushed.Add(int64(len(batch)))
}

// Stats returns the counters. A nil emitter reports zeros.
func (e *Emitter) Stats() Stats {
	if e == nil {
		return Stats{}
	}
	var invalid int64
	for i := range e.invalid {
		invalid += e.invalid[i].Load()
	}
	return Stats{
		Accepted:       e.accepted.Load(),
		Flushed:        e.flushed.Load(),
		Invalid:        invalid,
		AttrDropped:    e.attrDropped.Load(),
		DroppedQueue:   e.droppedQueue.Load(),
		DroppedSink:    e.droppedSink.Load(),
		DroppedBreaker: e.droppedBrk.Load(),
		SinkErrors:     e.sinkErrors.Load(),
	}
}

// Queued reports how many events wait for the batcher.
func (e *Emitter) Queued() int {
	if e == nil {
		return 0
	}
	return len(e.queue)
}

// Discard is a Sink that accepts and forgets every batch. It is for tests and
// for measuring the emitter without a backend.
var Discard Sink = SinkFunc(func(context.Context, []Event) error { return nil })
