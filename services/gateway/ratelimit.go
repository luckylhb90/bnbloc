package gateway

import (
	"sync"
	"time"
)

// rateLimiter is a per-key token bucket: `rate` tokens/sec, up to `burst`
// tokens. It bounds how often one authenticated subject may hit cost-bearing
// write endpoints (chat, borrow), so a client cannot spam messages or drive
// unbounded agent turns. Buckets live in each gateway replica's memory, so
// with N replicas behind a load balancer a key could spend N budgets: each
// replica therefore enforces 1/N of the configured rate and burst, N being
// the live replica count (BNBLOC_GATEWAY_PEERS; 1 when unset). This is the
// standard local approximation of a global limit and needs no shared store.
type rateLimiter struct {
	mu    sync.Mutex
	rate  float64
	burst float64
	// replicas returns the live gateway replica count (nil = 1).
	replicas func() int
	tokens   map[string]*bucket
	now      func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(ratePerSec, burst float64, replicas func() int) *rateLimiter {
	return &rateLimiter{
		rate:     ratePerSec,
		burst:    burst,
		replicas: replicas,
		tokens:   map[string]*bucket{},
		now:      time.Now,
	}
}

// share returns this replica's part of the configured rate and burst: both
// divided by the live replica count. The burst never drops below one token,
// or no request could ever pass.
func (l *rateLimiter) share() (rate, burst float64) {
	n := 1
	if l.replicas != nil {
		n = max(1, l.replicas())
	}
	return l.rate / float64(n), max(1, l.burst/float64(n))
}

// allow consumes one token for key, refilling by elapsed time. It returns false
// when the bucket is empty (caller should reject with 429).
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rate, burst := l.share()
	now := l.now()
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

// blocked reports, without consuming, whether key's bucket is empty right
// now. It lets a caller refuse up front while charging only some outcomes
// (a public share token miss charges the guess bucket; a hit does not).
func (l *rateLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.tokens[key]
	if b == nil {
		return false
	}
	rate, _ := l.share()
	tokens := b.tokens + l.now().Sub(b.last).Seconds()*rate
	return tokens < 1
}

// lazyLimiter builds its limiter (and the idle sweeper) on first use, so a
// Service needs no extra wiring per limit.
type lazyLimiter struct {
	once sync.Once
	l    *rateLimiter
}

func (z *lazyLimiter) get(ratePerSec, burst float64, replicas func() int) *rateLimiter {
	z.once.Do(func() {
		z.l = newRateLimiter(ratePerSec, burst, replicas)
		z.l.startSweeper()
	})
	return z.l
}

// startSweeper launches a background goroutine that periodically evicts buckets
// idle longer than bucketMaxIdle, so the per-key map cannot grow without bound
// as distinct subjects/IPs come and go (fixes the R6 memory leak). It is safe
// to call once per limiter; the goroutine lives for the process lifetime, which
// matches the limiter's own lifetime.
func (l *rateLimiter) startSweeper() {
	go func() {
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			l.sweep()
		}
	}()
}

// sweep removes buckets untouched for longer than bucketMaxIdle. A fully
// refilled idle bucket carries no state worth keeping, so dropping it is
// equivalent to never having seen the key.
func (l *rateLimiter) sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-bucketMaxIdle)
	for key, b := range l.tokens {
		if b.last.Before(cutoff) {
			delete(l.tokens, key)
		}
	}
}

// defaultChatRate / defaultChatBurst bound cost-bearing writes per subject:
// sustained ~2/sec with a burst of 10, enough for brisk human typing and
// legitimate agent addressing, but not spam or runaway automation.
const (
	defaultChatRate  = 2.0
	defaultChatBurst = 10.0
)

// defaultGuessRate / defaultGuessBurst bound brute-forceable attempts (login
// and the API-token exchange per client IP, invite redemption per subject):
// ~5/min sustained with a burst of 10, enough for a human retrying a typo but
// far below a credential-stuffing / code-guessing rate.
const (
	defaultGuessRate  = 5.0 / 60.0
	defaultGuessBurst = 10.0
)

// defaultPublicRate / defaultPublicBurst bound the unauthenticated public
// share reads per client IP: a page load fetches the metadata, the content
// and a site's files, so the burst covers one load of a typical static app.
const (
	defaultPublicRate  = 10.0
	defaultPublicBurst = 40.0
)

// sweepInterval / bucketMaxIdle control eviction of idle buckets so the per-key
// map cannot leak: buckets untouched for longer than bucketMaxIdle are dropped
// every sweepInterval.
const (
	sweepInterval = 5 * time.Minute
	bucketMaxIdle = 15 * time.Minute
)
