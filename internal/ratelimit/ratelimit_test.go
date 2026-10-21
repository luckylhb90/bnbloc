package ratelimit

import (
	"testing"
	"time"
)

// The limiter allows up to burst immediately, then refuses until tokens refill
// at the configured rate.
func TestRateLimiterBurstThenRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(2, 3, nil) // 2/sec, burst 3
	l.Now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.Allow("alice") {
			t.Fatalf("burst token %d denied, want allowed", i)
		}
	}
	if l.Allow("alice") {
		t.Fatal("4th call within the same instant allowed, want denied")
	}
	// After 1s, ~2 tokens refill.
	now = now.Add(time.Second)
	if !l.Allow("alice") || !l.Allow("alice") {
		t.Fatal("expected two refilled tokens after 1s")
	}
	if l.Allow("alice") {
		t.Fatal("third call after 1s allowed, want denied (only ~2 refilled)")
	}
}

// Buckets are per key, so one subject exhausting its tokens does not affect
// another.
func TestRateLimiterPerKey(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(1, 1, nil)
	l.Now = func() time.Time { return now }
	if !l.Allow("alice") || l.Allow("alice") {
		t.Fatal("alice: want first allowed, second denied")
	}
	if !l.Allow("bob") {
		t.Fatal("bob should have a full independent bucket")
	}
}

// With N live replicas each enforces 1/N of the configured rate and
// burst, so a subject spreading requests over every replica gets the
// configured budget in total, not N times it. The share follows the live
// count: a replica joining shrinks the bucket at once.
func TestRateLimiterDividesByLiveReplicas(t *testing.T) {
	now := time.Unix(0, 0)
	replicas := 3
	l := New(3, 9, func() int { return replicas }) // 3/s, burst 9 deployment-wide
	l.Now = func() time.Time { return now }
	allowed := 0
	for l.Allow("maya") {
		allowed++
	}
	if allowed != 3 {
		t.Fatalf("burst on one of 3 replicas = %d, want 3 (9/3)", allowed)
	}
	now = now.Add(time.Second) // refills 1 token (3/s / 3)
	if !l.Allow("maya") || l.Allow("maya") {
		t.Fatal("refill on one of 3 replicas is not 1 token/s")
	}
	// Back to one replica: the full budget applies again.
	replicas = 1
	now = now.Add(10 * time.Second)
	allowed = 0
	for l.Allow("maya") {
		allowed++
	}
	if allowed != 9 {
		t.Fatalf("burst on a single replica = %d, want 9", allowed)
	}
	// The burst never falls below one token (a budget smaller than the
	// replica count still lets each replica pass a request).
	replicas = 50
	now = now.Add(time.Minute)
	if !l.Allow("jack") {
		t.Fatal("a tiny per-replica share refused every request")
	}
}

// Sweep drops buckets idle longer than BucketMaxIdle and keeps the others.
func TestSweepDropsIdleBuckets(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(1, 1, nil)
	l.Now = func() time.Time { return now }
	l.Allow("old")
	now = now.Add(BucketMaxIdle)
	l.Allow("new")
	now = now.Add(time.Millisecond)
	l.Sweep()
	if l.Len() != 1 || !l.Blocked("new") {
		t.Fatalf("after a sweep %d bucket(s) remain; want only the recent one", l.Len())
	}
}
