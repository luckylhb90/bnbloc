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
	EnvURL     = "BNBLOC_PULSE_REDIS_URL"
	EnvKey     = "BNBLOC_PULSE_KEY"
	EnvCluster = "BNBLOC_PULSE_REDIS_CLUSTER"
)

// KeyPrefix is the hash tag every Pulse key carries. Redis Cluster places a key
// by the hash tag in braces, so with one tag all of Pulse's keys share a shard:
// a pipeline may touch any of them, which a cluster otherwise rejects (CROSSSLOT)
// or redirects (MOVED). Pulse's state is small, so one shard is enough.
const KeyPrefix = "{pulse}:"

// Stream is the Redis stream the emitter writes and the consumer reads.
const Stream = KeyPrefix + "events"

// NewRedis returns the client for Pulse's stream and live state. cluster selects
// a Redis Cluster client (BNBLOC_PULSE_REDIS_CLUSTER); the URL then names a
// cluster node or the configuration endpoint. timeout, when set, bounds dial,
// read and write (the emitter's fire-and-forget writes); zero keeps the
// library's defaults, which the consumer's blocking reads need.
func NewRedis(url string, cluster bool, timeout time.Duration) (redis.UniversalClient, error) {
	if cluster {
		opts, err := redis.ParseClusterURL(url)
		if err != nil {
			return nil, err
		}
		if timeout > 0 {
			opts.DialTimeout, opts.ReadTimeout, opts.WriteTimeout = timeout, timeout, timeout
			opts.PoolSize = 8
		}
		return redis.NewClusterClient(opts), nil
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		opts.DialTimeout, opts.ReadTimeout, opts.WriteTimeout = timeout, timeout, timeout
		opts.PoolSize = 8
	}
	return redis.NewClient(opts), nil
}

// ClusterFromEnv reads BNBLOC_PULSE_REDIS_CLUSTER: unset or "false" is a single
// node, "true" a Redis Cluster. Anything else is an error, so a typo fails at boot.
func ClusterFromEnv() (bool, error) {
	v := strings.TrimSpace(os.Getenv(EnvCluster))
	switch v {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s: want true or false, got %q", EnvCluster, v)
	}
}

// DefaultStreamMaxLen bounds the stream (approximate trimming). At the default
// the stream holds roughly the last hour of a busy organization's events; a
// consumer that is down for longer loses the oldest counts, by design: there
// is no reconciler (docs/prd/2026-10-24-executive-pulse-backend.md, section 6).
const DefaultStreamMaxLen = 500000

// NewRedisSink writes each batch as one pipelined round trip: one XADD per event,
// the envelope JSON in field "e".
func NewRedisSink(rdb redis.UniversalClient, stream string, maxLen int64) Sink {
	return redisSink{rdb: rdb, stream: stream, maxLen: maxLen}
}

type redisSink struct {
	rdb    redis.UniversalClient
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
	cluster, err := ClusterFromEnv()
	if err != nil {
		return nil, nil, err
	}
	rdb, err := NewRedis(url, cluster, 2*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", EnvURL, err)
	}
	key := []byte(os.Getenv(EnvKey))
	if len(key) != 0 && len(key) < MinKeyLen {
		return nil, nil, fmt.Errorf("%s: %w", EnvKey, ErrShortKey)
	}
	em := New(NewRedisSink(rdb, Stream, DefaultStreamMaxLen), Config{})
	go em.Run(ctx)
	if len(key) == 0 {
		log.Printf("pulse: %s: %s unset: presence events are off (member ids cannot be hashed)", service, EnvKey)
		key = nil
	}
	log.Printf("pulse: %s: emitting to stream %s", service, Stream)
	return em, key, nil
}
