// Package redisbus is the realtime bus (realtime.Bus) over Redis PUB/SUB:
// how realtime replicas without owners share a room's events
// (docs/prd/2026-10-30-replicas-without-owners.md §3). One endpoint per
// process holds one client for publishing and one subscriber connection
// for every room it receives.
package redisbus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/redis/go-redis/v9"
)

// EnvURL names the bus's Redis (redis://…). Unset: realtime has no bus (one
// replica).
const EnvURL = "BNBLOC_REALTIME_BUS_REDIS_URL"

// channelPrefix starts every room's channel.
const channelPrefix = "bnbloc:rt:room:"

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
	client *redis.Client
	ps     *redis.PubSub
	origin string

	mu sync.Mutex
	// rooms maps a channel to its subscription.
	rooms map[string]*room
	stop  context.CancelFunc
	done  chan struct{}
}

type room struct {
	deliver func(wire.Event)
	lost    func()
	// confirmed is closed when Redis confirmed the subscription.
	confirmed chan struct{}
	ok        bool
}

// New connects to the Redis at url and starts the endpoint's receive loop.
func New(url string) (*Bus, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvURL, err)
	}
	client := redis.NewClient(opts)
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
		rooms: map[string]*room{}, stop: stop, done: make(chan struct{})}
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

// Publish sends ev to the room's channel.
func (b *Bus) Publish(ev wire.Event) error {
	if len(ev.Encoded) == 0 {
		return fmt.Errorf("realtime bus: %s for room %s is not encoded", ev.Kind, ev.RoomID)
	}
	msg := make([]byte, 0, originLen+len(ev.Encoded))
	msg = append(append(msg, b.origin...), ev.Encoded...)
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	return b.client.Publish(ctx, channelPrefix+ev.RoomID, msg).Err()
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

// Subscribe subscribes the endpoint's connection to the room's channel and
// returns once Redis confirmed it.
func (b *Bus) Subscribe(roomID string, deliver func(wire.Event), lost func()) (func(), error) {
	ch := channelPrefix + roomID
	r := &room{deliver: deliver, lost: lost, confirmed: make(chan struct{})}
	b.mu.Lock()
	if _, taken := b.rooms[ch]; taken {
		b.mu.Unlock()
		return nil, fmt.Errorf("realtime bus: room %s is subscribed already", roomID)
	}
	b.rooms[ch] = r
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
		return nil, fmt.Errorf("realtime bus: subscribe room %s: %w", roomID, err)
	}
	return func() { b.forget(ch, r) }, nil
}

// forget ends r's subscription of ch.
func (b *Bus) forget(ch string, r *room) {
	b.mu.Lock()
	if b.rooms[ch] != r {
		b.mu.Unlock()
		return
	}
	delete(b.rooms, ch)
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), subscribeTimeout)
	defer cancel()
	if err := b.ps.Unsubscribe(ctx, ch); err != nil {
		log.Printf("realtime bus: unsubscribe %s: %v", ch, err)
	}
}

// receive reads the subscriber connection until Close. A read error means
// the connection broke: the next read connects again and subscribes every
// channel again, and each confirmation tells its room it may have missed
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
	r := b.rooms[ch]
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

// dispatch delivers one message to its room, unless this endpoint sent it.
func (b *Bus) dispatch(ch, payload string) {
	if len(payload) < originLen || payload[:originLen] == b.origin {
		return
	}
	b.mu.Lock()
	r := b.rooms[ch]
	b.mu.Unlock()
	if r == nil {
		return
	}
	raw := []byte(payload[originLen:])
	var ev wire.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		log.Printf("realtime bus: %s: undecodable event: %v", ch, err)
		return
	}
	ev.Encoded = raw
	r.deliver(ev)
}
