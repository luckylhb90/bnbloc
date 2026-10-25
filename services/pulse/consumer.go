package pulsesvc

import (
	"context"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// ConsumerConfig is the consumer's tuning. Zero fields take the defaults below.
type ConsumerConfig struct {
	Stream     string
	Group      string
	Name       string        // this consumer's name in the group: stable across restarts
	FlushEvery time.Duration // the rollup is written, and messages acked, this often
	BatchSize  int64
	MaxPending int           // unacked messages before reading pauses (back-pressure)
	RecentIDs  int           // event ids remembered to drop redeliveries
	PruneEvery time.Duration // live-state pruning
	Retention  time.Duration // rollup rows older than this are deleted hourly
}

const (
	defaultGroup      = "pulse"
	defaultName       = "pulse-1"
	defaultFlush      = 15 * time.Second
	defaultBatch      = 500
	defaultMaxPending = 100_000
	defaultRecent     = 200_000
	defaultPrune      = 10 * time.Second
	defaultRetention  = 90 * 24 * time.Hour
	ackChunk          = 1000
)

// Stats are the consumer's counters, read by the metrics sampler.
type Stats struct {
	Applied     atomic.Int64 // events counted and applied to the live state
	Duplicates  atomic.Int64 // redeliveries recognised by id and skipped
	Invalid     atomic.Int64 // entries the schema refused (dropped, acked)
	Flushed     atomic.Int64 // successful rollup flushes
	FlushErrors atomic.Int64
	Pending     atomic.Int64 // messages waiting for a flush before their ack
	LiveErrors  atomic.Int64 // live-state pipelines that failed (counted, not retried)
}

// Consumer reads the Pulse stream, counts it and acks it.
//
// Delivery is at-least-once. A message is acked only after the rollup that
// counted it has committed, so a crash before the commit redelivers it. A crash
// after the commit but before the ack can count it twice: that window is the
// known trade-off, and the board's totals are a dashboard, not a bill.
type Consumer struct {
	cfg    ConsumerConfig
	rdb    *redis.Client
	live   *Live
	roll   *Rollup
	rows   store.PulseMinutes
	recent *recentIDs
	stats  Stats
	// pending holds the stream ids read since the last successful flush.
	pending    []string
	lastFlush  time.Time
	lastRetain time.Time
	lastPrune  time.Time
	now        func() time.Time
}

// NewConsumer returns a consumer over rdb, writing rollups to rows.
func NewConsumer(rdb *redis.Client, rows store.PulseMinutes, cfg ConsumerConfig) *Consumer {
	if cfg.Stream == "" {
		cfg.Stream = pulse.Stream
	}
	if cfg.Group == "" {
		cfg.Group = defaultGroup
	}
	if cfg.Name == "" {
		cfg.Name = defaultName
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = defaultFlush
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatch
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = defaultMaxPending
	}
	if cfg.RecentIDs <= 0 {
		cfg.RecentIDs = defaultRecent
	}
	if cfg.PruneEvery <= 0 {
		cfg.PruneEvery = defaultPrune
	}
	if cfg.Retention <= 0 {
		cfg.Retention = defaultRetention
	}
	return &Consumer{
		cfg:    cfg,
		rdb:    rdb,
		live:   NewLive(rdb),
		roll:   NewRollup(),
		rows:   rows,
		recent: newRecentIDs(cfg.RecentIDs),
		now:    time.Now,
	}
}

// Stats returns the counters.
func (c *Consumer) Stats() *Stats { return &c.stats }

// Run consumes until ctx is done, then flushes once more.
func (c *Consumer) Run(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.cfg.Stream, c.cfg.Group, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return err
	}
	// Messages this consumer read but never acked (a restart) come back first.
	c.recover(ctx)
	c.lastFlush, c.lastPrune, c.lastRetain = c.now(), c.now(), c.now()
	for {
		if ctx.Err() != nil {
			c.flush(context.Background())
			return nil
		}
		if len(c.pending) < c.cfg.MaxPending {
			c.readBatch(ctx, ">")
		} else {
			// Too much unflushed: wait for the flush rather than read on.
			time.Sleep(100 * time.Millisecond)
		}
		now := c.now()
		if now.Sub(c.lastFlush) >= c.cfg.FlushEvery {
			c.flush(ctx)
			c.lastFlush = now
		}
		if now.Sub(c.lastPrune) >= c.cfg.PruneEvery {
			if err := c.live.Prune(ctx, now); err != nil && ctx.Err() == nil {
				log.Printf("pulse: prune live state: %v", err)
			}
			c.lastPrune = now
		}
		if now.Sub(c.lastRetain) >= time.Hour {
			c.retain(ctx, now)
			c.lastRetain = now
		}
	}
}

// recover re-reads this consumer's pending entries (the group's history from id
// 0) and advances a cursor past each batch, so it ends once nothing is left. The
// cursor matters: the same batch comes back until the entries are acked.
func (c *Consumer) recover(ctx context.Context) {
	cursor := "0"
	for {
		n, last := c.readBatch(ctx, cursor)
		if n == 0 {
			return
		}
		cursor = last
	}
}

// readBatch reads up to BatchSize messages from id (">" for new ones; an id for
// this consumer's own pending history). It returns how many it read and the id
// of the last one, the cursor for the next read.
func (c *Consumer) readBatch(ctx context.Context, id string) (int, string) {
	res, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    c.cfg.Group,
		Consumer: c.cfg.Name,
		Streams:  []string{c.cfg.Stream, id},
		Count:    c.cfg.BatchSize,
		Block:    time.Second,
	}).Result()
	if err == redis.Nil || ctx.Err() != nil {
		return 0, id
	}
	if err != nil {
		log.Printf("pulse: read stream: %v", err)
		time.Sleep(time.Second)
		return 0, id
	}
	pipe := c.rdb.Pipeline()
	n, last := 0, id
	for _, s := range res {
		for _, m := range s.Messages {
			n++
			last = m.ID
			c.handle(ctx, pipe, m)
		}
	}
	if n > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			// The live state is best effort and self-healing (presence refreshes
			// every 30 s; a claim missed here is corrected by its finish). The
			// rollup is not affected.
			c.stats.LiveErrors.Add(1)
			log.Printf("pulse: live state write: %v", err)
		}
	}
	c.stats.Pending.Store(int64(len(c.pending)))
	return n, last
}

func (c *Consumer) handle(ctx context.Context, pipe redis.Pipeliner, m redis.XMessage) {
	c.pending = append(c.pending, m.ID)
	raw, _ := m.Values["e"].(string)
	ev, err := pulse.Decode([]byte(raw))
	if err != nil {
		c.stats.Invalid.Add(1)
		return
	}
	if c.recent.Seen(ev.ID) {
		c.stats.Duplicates.Add(1)
		return
	}
	c.roll.Add(ev)
	c.live.Apply(pipe, ctx, ev)
	c.stats.Applied.Add(1)
}

// flush writes the rollup and, only after it committed, acks the messages it
// counted. A failed write keeps the deltas and the pending ids for the next try.
func (c *Consumer) flush(ctx context.Context) {
	if rows := c.roll.Drain(); len(rows) > 0 {
		if err := c.rows.AddPulseMinutes(ctx, rows, c.now().UnixMilli()); err != nil {
			c.roll.Restore(rows)
			c.stats.FlushErrors.Add(1)
			log.Printf("pulse: rollup write failed, will retry: %v", err)
			return
		}
	}
	for start := 0; start < len(c.pending); start += ackChunk {
		ids := c.pending[start:min(start+ackChunk, len(c.pending))]
		if err := c.rdb.XAck(ctx, c.cfg.Stream, c.cfg.Group, ids...).Err(); err != nil {
			log.Printf("pulse: ack: %v", err)
			return // the unacked ids stay pending and are acked by the next flush
		}
	}
	c.pending = c.pending[:0]
	c.stats.Pending.Store(0)
	c.stats.Flushed.Add(1)
}

// retain deletes rollup rows older than the retention window.
func (c *Consumer) retain(ctx context.Context, now time.Time) {
	before := now.Add(-c.cfg.Retention).UnixMilli()
	if n, err := c.rows.PrunePulseMinutes(ctx, before); err != nil {
		log.Printf("pulse: retention: %v", err)
	} else if n > 0 {
		log.Printf("pulse: retention deleted %d rollup rows", n)
	}
}

// recentIDs remembers the last N event ids, for dropping redeliveries. It is
// bounded: an id older than the window can be counted again only if it is
// redelivered after that many newer events.
type recentIDs struct {
	max  int
	ring []string
	set  map[string]struct{}
	next int
}

func newRecentIDs(n int) *recentIDs {
	return &recentIDs{max: n, set: make(map[string]struct{}, n)}
}

// Seen reports whether id was seen recently, and remembers it.
func (r *recentIDs) Seen(id string) bool {
	if _, ok := r.set[id]; ok {
		return true
	}
	if len(r.ring) < r.max {
		r.ring = append(r.ring, id)
	} else {
		delete(r.set, r.ring[r.next])
		r.ring[r.next] = id
		r.next = (r.next + 1) % r.max
	}
	r.set[id] = struct{}{}
	return false
}
