package redisbus

import (
	"context"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// The census of a service's replicas (a budget shared out per replica
// divides by it) and the rendezvous records relay pairs its streams by.

const (
	censusPrefix     = "bnbloc:replicas:"
	rendezvousPrefix = "bnbloc:meet:"
	// censusBeat is how often a replica says it is alive; one not heard for
	// censusGone is counted out.
	censusBeat = 5 * time.Second
	censusGone = 3 * censusBeat
)

// Census counts a service's live replicas, this one included.
type Census struct {
	n atomic.Int64
}

// Count is the live replica count, at least 1.
func (c *Census) Count() int {
	if c == nil {
		return 1
	}
	return int(max(c.n.Load(), 1))
}

// Census starts counting service's replicas: this endpoint beats every
// censusBeat for the process's life and reads the count back.
func (b *Bus) Census(service string) *Census {
	c := &Census{}
	c.n.Store(1)
	key := censusPrefix + service
	beat := func() {
		ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
		defer cancel()
		now := time.Now().UnixMilli()
		var card *redis.IntCmd
		_, err := b.client.Pipelined(ctx, func(p redis.Pipeliner) error {
			p.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: b.origin})
			p.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now-censusGone.Milliseconds(), 10))
			card = p.ZCard(ctx, key)
			p.PExpire(ctx, key, censusGone)
			return nil
		})
		if err != nil {
			log.Printf("redisbus: census of %s: %v (keeping the last count)", service, err)
			return
		}
		c.n.Store(card.Val())
	}
	beat()
	go func() {
		for range time.Tick(censusBeat) {
			beat()
		}
	}()
	return c
}

// Meet records holder at key for ttl unless another holder is there, and
// returns the holder at key now: holder itself when it was first.
func (b *Bus) Meet(key, holder string, ttl time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	k := rendezvousPrefix + key
	ok, err := b.client.SetNX(ctx, k, holder, ttl).Result()
	if err != nil || ok {
		return holder, err
	}
	at, err := b.client.Get(ctx, k).Result()
	if err == redis.Nil {
		return b.Meet(key, holder, ttl) // it ended between the two calls
	}
	return at, err
}

// leaveScript deletes the key only while it still names the holder.
var leaveScript = redis.NewScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end return 0`)

// Leave deletes key's record if it is still holder's.
func (b *Bus) Leave(key, holder string) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	return leaveScript.Run(ctx, b.client, []string{rendezvousPrefix + key}, holder).Err()
}
