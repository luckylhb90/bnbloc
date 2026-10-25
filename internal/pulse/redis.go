package pulse

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Deployment settings. An empty EnvURL turns Pulse off: no connection, no
// goroutine, and every product hook stays a no-op.
const (
	EnvURL = "BNBLOC_PULSE_REDIS_URL"
	EnvKey = "BNBLOC_PULSE_KEY"
)

// Stream is the Redis stream the emitter writes and the consumer reads.
const Stream = "pulse:events"

// DefaultStreamMaxLen bounds the stream (approximate trimming). At the default
// the stream holds roughly the last hour of a busy organization's events; a
// consumer that is down for longer loses the oldest counts, by design: there
// is no reconciler (docs/prd/2026-10-24-executive-pulse-backend.md, section 6).
const DefaultStreamMaxLen = 500000

// NewRedisSink writes each batch as one pipelined round trip: one XADD per event,
// the envelope JSON in field "e".
func NewRedisSink(rdb *redis.Client, stream string, maxLen int64) Sink {
	return redisSink{rdb: rdb, stream: stream, maxLen: maxLen}
}

type redisSink struct {
	rdb    *redis.Client
	stream string
	maxLen int64
}

func (s redisSink) Write(ctx context.Context, batch []Event) error {
	pipe := s.rdb.Pipeline()
	for _, ev := range batch {
		raw, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("pulse: encode: %w", err)
		}
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: s.stream,
			MaxLen: s.maxLen,
			Approx: true,
			Values: map[string]any{"e": raw},
		})
	}
	_, err := pipe.Exec(ctx)
	return err
}

// FromEnv starts Pulse for one service: the emitter, its batcher (until ctx is
// done) and the member key. With no URL it returns nil, nil, nil. A set URL or
// key that does not parse is an error, so a misconfigured deployment fails at
// boot rather than silently dropping every event.
func FromEnv(ctx context.Context, service string) (*Emitter, []byte, error) {
	url := strings.TrimSpace(os.Getenv(EnvURL))
	if url == "" {
		return nil, nil, nil
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", EnvURL, err)
	}
	opts.DialTimeout = 2 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second
	opts.PoolSize = 8
	key := []byte(os.Getenv(EnvKey))
	if len(key) != 0 && len(key) < MinKeyLen {
		return nil, nil, fmt.Errorf("%s: %w", EnvKey, ErrShortKey)
	}
	em := New(NewRedisSink(redis.NewClient(opts), Stream, DefaultStreamMaxLen), Config{})
	go em.Run(ctx)
	if len(key) == 0 {
		log.Printf("pulse: %s: %s unset: presence events are off (member ids cannot be hashed)", service, EnvKey)
		key = nil
	}
	log.Printf("pulse: %s: emitting to stream %s", service, Stream)
	return em, key, nil
}
