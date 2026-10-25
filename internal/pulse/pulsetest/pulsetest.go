// Package pulsetest records what a Pulse emitter sends, for tests in the packages
// that emit. Production code never imports it.
package pulsetest

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/pulse"
)

// Recorder keeps every event the emitter's sink received, as decoded JSON.
type Recorder struct {
	mu     sync.Mutex
	events []map[string]any
}

// New returns an emitter that writes to a recorder and a batcher running until
// the test ends. The flush interval is short, so tests read events quickly.
func New(t testing.TB) (*pulse.Emitter, *Recorder) {
	t.Helper()
	rec := &Recorder{}
	sink := pulse.SinkFunc(func(_ context.Context, batch []pulse.Event) error {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		for _, ev := range batch {
			raw, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return err
			}
			rec.events = append(rec.events, m)
		}
		return nil
	})
	e := pulse.New(sink, pulse.Config{FlushInterval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		<-e.Done()
	})
	go e.Run(ctx)
	return e, rec
}

// Events returns the events received so far.
func (r *Recorder) Events() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.events...)
}

// Wait returns once n events have been received (or fails the test after 5 s).
func (r *Recorder) Wait(t testing.TB, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := r.Events(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d pulse events, got %d", n, len(r.Events()))
		}
		time.Sleep(time.Millisecond)
	}
}

// Attr returns an event's attribute value, or nil.
func Attr(ev map[string]any, key string) any {
	attrs, _ := ev["attrs"].(map[string]any)
	return attrs[key]
}
