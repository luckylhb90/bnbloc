package pulsesvc

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// fakeRows is the real in-memory rollup store, with failures injected on demand:
// the consumer's retry and no-double-count rules are tested against the same
// semantics production runs.
type fakeRows struct {
	*store.MemoryPulseMinutes
	mu       sync.Mutex
	failNext int
}

func newFakeRows() *fakeRows {
	return &fakeRows{MemoryPulseMinutes: store.NewMemoryPulseMinutes()}
}

// AddPulseMinutes fails the next failNext writes, then delegates.
func (f *fakeRows) AddPulseMinutes(ctx context.Context, rows []store.PulseMinuteDelta, nowMs int64) error {
	f.mu.Lock()
	if f.failNext > 0 {
		f.failNext--
		f.mu.Unlock()
		return errors.New("injected write failure")
	}
	f.mu.Unlock()
	return f.MemoryPulseMinutes.AddPulseMinutes(ctx, rows, nowMs)
}

// total sums one metric across dimensions and minutes for a tenant.
func (f *fakeRows) total(tenant, metric string) float64 {
	totals, _ := f.PulseTotals(context.Background(), tenant, 0, 1<<62, []string{metric})
	return totals[metric]
}

// testRedis returns a client for the test Redis, or skips without one.
func testRedis(t *testing.T) redis.UniversalClient {
	t.Helper()
	url := os.Getenv("BNBLOC_TEST_REDIS_URL")
	if url == "" {
		t.Skip("BNBLOC_TEST_REDIS_URL not set")
	}
	cluster := os.Getenv("BNBLOC_TEST_REDIS_CLUSTER") == "true"
	rdb, err := pulse.NewRedis(url, cluster, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// waitFor polls cond for up to 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
