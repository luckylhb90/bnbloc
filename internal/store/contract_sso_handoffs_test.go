package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// A hand-off is single-use under concurrency, a live key cannot be parked
// twice, it is live strictly before its expiry (taken at expires-1, never
// at expires), and expired entries are pruned by the next put (their key is
// free again).
func TestContractSSOHandoffStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.SSOHandoffs
		ctx := context.Background()
		const now = int64(1_000_000)
		key := uniq("key-")
		h := SSOHandoff{KeyHash: key, NonceHash: uniq("nonce-"), Subject: "maya", Email: "maya@example.com", TenantHint: "acme", ExpiresAtMs: now + 60_000}
		if err := s.PutSSOHandoff(ctx, h, now); err != nil {
			t.Fatal(err)
		}
		if err := s.PutSSOHandoff(ctx, h, now); !errors.Is(err, ErrSSOHandoffPending) {
			t.Fatalf("second put err = %v; want ErrSSOHandoffPending", err)
		}
		var mu sync.Mutex
		wins := 0
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, ok, err := s.TakeSSOHandoff(ctx, key, now+1)
				if err != nil {
					t.Errorf("take: %v", err)
					return
				}
				if ok {
					mu.Lock()
					wins++
					mu.Unlock()
					if got != h {
						t.Errorf("taken = %+v, want %+v", got, h)
					}
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("winners = %d; want exactly 1", wins)
		}

		// The expiry boundary: live at expires-1, gone at expires.
		edge := SSOHandoff{KeyHash: uniq("edge-"), NonceHash: "n", Subject: "maya", ExpiresAtMs: now + 100}
		if err := s.PutSSOHandoff(ctx, edge, now); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.TakeSSOHandoff(ctx, edge.KeyHash, edge.ExpiresAtMs); err != nil || ok {
			t.Fatalf("take at expiry = %v, %v; want not found", ok, err)
		}
		edge.KeyHash = uniq("edge2-")
		if err := s.PutSSOHandoff(ctx, edge, now); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.TakeSSOHandoff(ctx, edge.KeyHash, edge.ExpiresAtMs-1); err != nil || !ok {
			t.Fatalf("take just before expiry = %v, %v; want found", ok, err)
		}

		// An entry expired at the next put is pruned: its key is free.
		stale := SSOHandoff{KeyHash: uniq("stale-"), NonceHash: "n", Subject: "maya", ExpiresAtMs: now + 10}
		if err := s.PutSSOHandoff(ctx, stale, now); err != nil {
			t.Fatal(err)
		}
		stale.ExpiresAtMs = now + 9000
		if err := s.PutSSOHandoff(ctx, stale, now+10); err != nil {
			t.Fatalf("put over an entry expired at now = %v", err)
		}
		if got, ok, err := s.TakeSSOHandoff(ctx, stale.KeyHash, now+20); err != nil || !ok || got.ExpiresAtMs != now+9000 {
			t.Fatalf("take of the re-parked entry = %+v %v %v", got, ok, err)
		}
		if _, ok, err := s.TakeSSOHandoff(ctx, uniq("missing-"), now); err != nil || ok {
			t.Fatalf("take of a missing key = %v, %v", ok, err)
		}
	})
}
