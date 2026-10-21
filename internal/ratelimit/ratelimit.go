// Package ratelimit is bnbloc's one token bucket: per-key buckets of `rate`
// tokens a second up to `burst`, in a process's memory. The gateway bounds
// requests with it (a subject's writes, a client IP's guesses, an app's
// calls); realtime bounds the events a subscription and a source may set
// (docs/prd/2026-10-21-room-events.md §2.4).
//
// Buckets live in each replica's memory, so with N replicas behind a load
// balancer a key could spend N budgets: each replica therefore enforces 1/N
// of the configured rate and burst, N being the live replica count (the
// Replicas function; 1 when nil). This is the standard local approximation
// of a global limit and needs no shared store.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a per-key token bucket.
type Limiter struct {
	mu    sync.Mutex
	rate  float64
	burst float64
	// replicas returns the live replica count (nil = 1).
	replicas func() int
	tokens   map[string]*bucket
	// Now is the clock (tests set it).
	Now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New builds a limiter of ratePerSec tokens a second up to burst, shared
// across replicas() replicas (nil: one).
func New(ratePerSec, burst float64, replicas func() int) *Limiter {
	return &Limiter{
		rate:     ratePerSec,
		burst:    burst,
		replicas: replicas,
		tokens:   map[string]*bucket{},
		Now:      time.Now,
	}
}

// share returns this replica's part of the configured rate and burst: both
// divided by the live replica count. The burst never drops below one token,
// or no request could ever pass.
func (l *Limiter) share() (rate, burst float64) {
	n := 1
	if l.replicas != nil {
		n = max(1, l.replicas())
	}
	return l.rate / float64(n), max(1, l.burst/float64(n))
}

// Allow consumes one token for key, refilling by elapsed time. It returns
// false when the bucket is empty (the caller refuses, e.g. with 429).
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rate, burst := l.share()
	now := l.Now()
	b := l.tokens[key]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		l.tokens[key] = b
	}
	// Refill by elapsed time, capped at this replica's burst (which shrinks
	// at once when a replica joins).
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += elapsed * rate
	if b.tokens > burst {
		b.tokens = burst
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Blocked reports, without consuming, whether key's bucket is empty right
// now. It lets a caller refuse up front while charging only some outcomes
// (a public share token miss charges the guess bucket; a hit does not).
func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.tokens[key]
	if b == nil {
		return false
	}
	rate, _ := l.share()
	tokens := b.tokens + l.Now().Sub(b.last).Seconds()*rate
	return tokens < 1
}

// Lazy builds its limiter (and the idle sweeper) on first use, so a service
// needs no extra wiring per limit.
type Lazy struct {
	once sync.Once
	l    *Limiter
}

// Get is the limiter, built on the first call with these values.
func (z *Lazy) Get(ratePerSec, burst float64, replicas func() int) *Limiter {
	z.once.Do(func() {
		z.l = New(ratePerSec, burst, replicas)
		z.l.startSweeper()
	})
	return z.l
}

// SweepInterval / BucketMaxIdle control eviction of idle buckets so the
// per-key map cannot leak: buckets untouched for longer than BucketMaxIdle
// are dropped every SweepInterval.
const (
	SweepInterval = 5 * time.Minute
	BucketMaxIdle = 15 * time.Minute
)

// startSweeper launches a background goroutine that periodically evicts
// buckets idle longer than BucketMaxIdle, so the per-key map cannot grow
// without bound as distinct keys come and go. The goroutine lives for the
// process lifetime, which matches a Lazy limiter's own lifetime.
func (l *Limiter) startSweeper() {
	go func() {
		ticker := time.NewTicker(SweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			l.Sweep()
		}
	}()
}

// Sweep removes buckets untouched for longer than BucketMaxIdle. A fully
// refilled idle bucket carries no state worth keeping, so dropping it is
// equivalent to never having seen the key.
func (l *Limiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.Now().Add(-BucketMaxIdle)
	for key, b := range l.tokens {
		if b.last.Before(cutoff) {
			delete(l.tokens, key)
		}
	}
}

// Len is how many buckets the limiter holds (tests of the sweep).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.tokens)
}
