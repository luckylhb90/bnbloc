package pulse

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// The sink against a real Redis. Gated on BNBLOC_TEST_REDIS_URL; each test uses
// its own stream name, so a shared test Redis is safe.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("BNBLOC_TEST_REDIS_URL")
	if url == "" {
		t.Skip("BNBLOC_TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestRedisSinkWritesOneEntryPerEventInOneRoundTrip(t *testing.T) {
	rdb := testRedis(t)
	stream := "pulse:test:sink:" + time.Now().Format("150405.000000000")
	t.Cleanup(func() { rdb.Del(context.Background(), stream) })

	batch := validEvents()[:3]
	if err := NewRedisSink(rdb, stream, 1000).Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	entries, err := rdb.XRange(context.Background(), stream, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(batch) {
		t.Fatalf("stream has %d entries, want %d", len(entries), len(batch))
	}
	for i, e := range entries {
		raw, _ := e.Values["e"].(string)
		ev, err := Decode([]byte(raw))
		if err != nil {
			t.Fatalf("entry %d does not decode: %v", i, err)
		}
		if ev.ID != batch[i].ID {
			t.Fatalf("entry %d id = %q, want %q (order kept)", i, ev.ID, batch[i].ID)
		}
	}
}

// With no URL Pulse is off: no client, no goroutine (the kill switch, G6).
func TestFromEnvIsOffWithoutURL(t *testing.T) {
	t.Setenv(EnvURL, "")
	em, key, err := FromEnv(context.Background(), "test")
	if err != nil || em != nil || key != nil {
		t.Fatalf("FromEnv with no URL = (%v, %v, %v), want all nil", em, key, err)
	}
}

func TestFromEnvRefusesABadURLOrAShortKey(t *testing.T) {
	t.Setenv(EnvURL, "://not a url")
	if _, _, err := FromEnv(context.Background(), "test"); err == nil {
		t.Fatal("a URL that does not parse must fail at boot")
	}
	t.Setenv(EnvURL, "redis://127.0.0.1:1/0")
	t.Setenv(EnvKey, "too-short")
	if _, _, err := FromEnv(context.Background(), "test"); err == nil {
		t.Fatal("a member key shorter than 32 bytes must fail at boot")
	}
}

// With Redis down, the product's Emit stays fast and the loss is counted: the
// sink fails, the breaker opens, and nothing waits on the connection (P1 against
// the real client, not a stub). Port 1 has no listener.
func TestEmitStaysFastWhenRedisIsDown(t *testing.T) {
	opts := &redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, ReadTimeout: 200 * time.Millisecond, WriteTimeout: 200 * time.Millisecond}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	em := New(NewRedisSink(rdb, "pulse:test:down", 1000), Config{QueueSize: 256, BatchSize: 16, FlushInterval: time.Millisecond, WriteTimeout: 200 * time.Millisecond, BreakerThreshold: 2, BreakerCooldown: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	start := time.Now()
	for i := 0; i < 5000; i++ {
		em.Emit(validEvents()[4])
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("5000 emits took %s with Redis down: the product's path is waiting on Redis", elapsed)
	}
	waitUntil(t, func() bool { return em.Stats().SinkErrors >= 2 })
	if em.Stats().DroppedSink+em.Stats().DroppedBreaker+em.Stats().DroppedQueue == 0 {
		t.Fatal("events lost to a down Redis must be counted")
	}
}
