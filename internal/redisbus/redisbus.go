// Package redisbus is the one Redis the replicas of a bnbloc service share
// (BNBLOC_REDIS_URL): topics between replicas over PUB/SUB
// (realtime.Topics), claims and marks as keys, a census of the live
// replicas, and rendezvous records (relay). It is the only thing a service
// needs to run more than one replica: no peer lists, no ownership
// (docs/prd/2026-10-30-replicas-without-owners.md). One endpoint per
// process holds one client for publishing and keys and one subscriber
// connection for every topic it receives.
package redisbus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// EnvURL names the replicas' Redis (redis://…, rediss://… with TLS). Unset:
// the service runs one replica. EnvCluster ("true") says it is a Redis
// Cluster (cluster mode enabled): the URL names a node or the cluster's
// configuration endpoint.
const (
	EnvURL     = "BNBLOC_REDIS_URL"
	EnvCluster = "BNBLOC_REDIS_CLUSTER"
)

// retired are the settings replicas once needed besides a Redis: a
// deployment still carrying one is configured for a design that is gone.
var retired = []string{
	"BNBLOC_REALTIME_BUS_REDIS_URL", "BNBLOC_REALTIME_BUS_REDIS_CLUSTER",
	"BNBLOC_REALTIME_PEERS", "BNBLOC_REALTIME_SELF",
	"BNBLOC_RELAY_PEERS", "BNBLOC_RELAY_SELF", "BNBLOC_GATEWAY_PEERS",
}

// FromEnv connects to BNBLOC_REDIS_URL (nil when unset: one replica). A
// retired setting (a peer list, the realtime-only bus URL) is an error that
// names the fix, so a deployment configured for the old design does not
// start as replicas that never meet.
func FromEnv(getenv func(string) string) (*Bus, error) {
	for _, name := range retired {
		if getenv(name) != "" {
			return nil, fmt.Errorf("%s is no longer read: replicas share one Redis, %s (and %s=true for a Redis Cluster); remove %s", name, EnvURL, EnvCluster, name)
		}
	}
	url := getenv(EnvURL)
	if url == "" {
		return nil, nil
	}
	cluster := false
	switch v := getenv(EnvCluster); v {
	case "", "false", "0":
	case "true", "1":
		cluster = true
	default:
		return nil, fmt.Errorf("%s=%q: want true or false", EnvCluster, v)
	}
	return New(url, cluster)
}

// channelPrefix starts every topic's channel.
const channelPrefix = "bnbloc:rt:"

// originLen is the length of the origin that starts every message: the
// endpoint that published it, so it skips its own.
const originLen = 16

// Timeouts: a publish or a subscription that takes longer fails (the hub
// counts it and its streams read the difference).
const (
	publishTimeout   = 2 * time.Second
	subscribeTimeout = 5 * time.Second
	retryAfter       = 500 * time.Millisecond
)

// Bus is one process's endpoint of the bus.
type Bus struct {
	client redis.UniversalClient
	ps     *redis.PubSub
	origin string

	mu sync.Mutex
	// topics maps a channel to its subscription.
	topics map[string]*topic
	stop   context.CancelFunc
	done   chan struct{}
}

type topic struct {
	deliver func([]byte)
	lost    func()
	// confirmed is closed when Redis confirmed the subscription.
	confirmed chan struct{}
	ok        bool
}

// New connects to the Redis at url (a Redis Cluster when cluster is set)
// and starts the endpoint's receive loop.
//
// On a cluster the bus uses plain PUB/SUB, which the cluster delivers to
// subscribers on every node (a message crosses the cluster bus once per
// node): the subscriber connection may be on any node, and claims and marks
// are one key each, so no command spans slots.
func New(url string, cluster bool) (*Bus, error) {
	var client redis.UniversalClient
	if cluster {
		opts, err := redis.ParseClusterURL(url)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvURL, err)
		}
		client = redis.NewClusterClient(opts)
	} else {
		opts, err := redis.ParseURL(url)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvURL, err)
		}
		client = redis.NewClient(opts)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("realtime bus: redis: %w", err)
	}
	var id [originLen / 2]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	loop, stop := context.WithCancel(context.Background())
	b := &Bus{client: client, ps: client.Subscribe(loop), origin: hex.EncodeToString(id[:]),
		topics: map[string]*topic{}, stop: stop, done: make(chan struct{})}
	go b.receive(loop)
	return b, nil
}

// Close ends the endpoint.
func (b *Bus) Close() error {
	b.stop()
	err := b.ps.Close()
	<-b.done
	return errors.Join(err, b.client.Close())
}

// Publish sends msg on the topic's channel.
func (b *Bus) Publish(name string, msg []byte) error {
	out := make([]byte, 0, originLen+len(msg))
	out = append(append(out, b.origin...), msg...)
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	return b.client.Publish(ctx, channelPrefix+name, out).Err()
}

// claimPrefix starts every claim's key.
const claimPrefix = "bnbloc:rt:claim:"

// Claim takes key for ttl unless it is taken (SET NX PX); when it is, wait
// is what is left of it (PTTL).
func (b *Bus) Claim(key string, ttl time.Duration) (bool, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	k := claimPrefix + key
	ok, err := b.client.SetNX(ctx, k, 1, ttl).Result()
	if err != nil || ok {
		return ok, 0, err
	}
	left, err := b.client.PTTL(ctx, k).Result()
	if err != nil {
		return false, 0, err
	}
	if left <= 0 {
		left = time.Millisecond // it ended between the two calls
	}
	return false, left, nil
}

// markPrefix starts every mark's key: a hash of holder -> the Unix ms its
// mark ends.
const markPrefix = "bnbloc:rt:mark:"

// Mark sets holder's mark of key for ttl (realtime.Marks); the key itself
// lives as long as its longest mark.
func (b *Bus) Mark(key, holder string, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	k := markPrefix + key
	_, err := b.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, k, holder, time.Now().Add(ttl).UnixMilli())
		p.PExpire(ctx, k, ttl)
		return nil
	})
	return err
}

// Unmark deletes holder's mark of key.
func (b *Bus) Unmark(key, holder string) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	return b.client.HDel(ctx, markPrefix+key, holder).Err()
}

// Marked reports whether any holder's mark of key is live.
func (b *Bus) Marked(key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	ends, err := b.client.HGetAll(ctx, markPrefix+key).Result()
	if err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	for _, end := range ends {
		if ms, err := strconv.ParseInt(end, 10, 64); err == nil && ms > now {
			return true, nil
		}
	}
	return false, nil
}

// Subscribe subscribes the endpoint's connection to the topic's channel and
// returns once Redis confirmed it.
func (b *Bus) Subscribe(name string, deliver func([]byte), lost func()) (func(), error) {
	ch := channelPrefix + name
	r := &topic{deliver: deliver, lost: lost, confirmed: make(chan struct{})}
	b.mu.Lock()
	if _, taken := b.topics[ch]; taken {
		b.mu.Unlock()
		return nil, fmt.Errorf("realtime bus: topic %s is subscribed already", name)
	}
	b.topics[ch] = r
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), subscribeTimeout)
	defer cancel()
	err := b.ps.Subscribe(ctx, ch)
	if err == nil {
		select {
		case <-r.confirmed:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		b.forget(ch, r)
		return nil, fmt.Errorf("realtime bus: subscribe %s: %w", name, err)
	}
	return func() { b.forget(ch, r) }, nil
}

// forget ends r's subscription of ch.
func (b *Bus) forget(ch string, r *topic) {
	b.mu.Lock()
	if b.topics[ch] != r {
		b.mu.Unlock()
		return
	}
	delete(b.topics, ch)
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), subscribeTimeout)
	defer cancel()
	if err := b.ps.Unsubscribe(ctx, ch); err != nil {
		log.Printf("realtime bus: unsubscribe %s: %v", ch, err)
	}
}

// receive reads the subscriber connection until Close. A read error means
// the connection broke: the next read connects again and subscribes every
// channel again, and each confirmation tells its topic it may have missed
// events (confirm), once Redis is back.
func (b *Bus) receive(ctx context.Context) {
	defer close(b.done)
	for {
		msg, err := b.ps.Receive(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("realtime bus: receive: %v (subscribing again)", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryAfter):
			}
			continue
		}
		switch m := msg.(type) {
		case *redis.Subscription:
			if m.Kind == "subscribe" {
				b.confirm(m.Channel)
			}
		case *redis.Message:
			b.dispatch(m.Channel, m.Payload)
		}
	}
}

// confirm marks ch's subscription in place; a confirmation of one that was
// in place already is a subscription made again on a new connection: what
// was published meanwhile is lost to it.
func (b *Bus) confirm(ch string) {
	b.mu.Lock()
	r := b.topics[ch]
	again := r != nil && r.ok
	if r != nil && !r.ok {
		r.ok = true
		close(r.confirmed)
	}
	b.mu.Unlock()
	if again {
		r.lost()
	}
}

// dispatch delivers one message to its topic, unless this endpoint sent it.
func (b *Bus) dispatch(ch, payload string) {
	if len(payload) < originLen || payload[:originLen] == b.origin {
		return
	}
	b.mu.Lock()
	r := b.topics[ch]
	b.mu.Unlock()
	if r != nil {
		r.deliver([]byte(payload[originLen:]))
	}
}
