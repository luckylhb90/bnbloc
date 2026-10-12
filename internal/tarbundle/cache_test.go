package tarbundle

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// cacheRaw packs a one-file site bundle and returns its archive and pin.
func cacheRaw(t *testing.T, body string) ([]byte, string) {
	t.Helper()
	raw, err := PackFiles(map[string][]byte{"index.html": []byte(body)}, SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(raw, "", SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw, b.SHA256
}

// load loads raw under pin, counting fetches.
func load(c *Cache, pin string, raw []byte, fetches *atomic.Int32) (*Bundle, error) {
	return c.LoadPinned(pin, SiteLimits, func() ([]byte, error) {
		if fetches != nil {
			fetches.Add(1)
		}
		return raw, nil
	})
}

// Every bundle load is pinned: an empty or malformed pin is refused before
// any fetch, bytes that do not match the pin are refused and never cached,
// and a cached bundle is only ever returned for its own pin.
func TestBundleLoadsArePinned(t *testing.T) {
	raw, pin := cacheRaw(t, "<h1>a</h1>")
	other, otherPin := cacheRaw(t, "<h1>b</h1>")
	cache := NewCache(1 << 20)
	var fetches atomic.Int32
	for _, bad := range []string{"", strings.ToUpper(pin), pin[:10], "sha256/" + pin} {
		if _, err := load(cache, bad, raw, &fetches); !errors.Is(err, ErrUnpinned) {
			t.Errorf("LoadPinned(%q) err = %v, want ErrUnpinned", bad, err)
		}
	}
	if n := fetches.Load(); n != 0 {
		t.Fatalf("an unpinned load fetched %d times", n)
	}
	if _, err := load(cache, otherPin, raw, &fetches); !errors.Is(err, ErrDigest) {
		t.Fatalf("mismatching bytes: err = %v, want ErrDigest", err)
	}
	// The mismatching bytes were not cached under either digest.
	b, err := load(cache, otherPin, other, &fetches)
	if err != nil || b.SHA256 != otherPin {
		t.Fatalf("LoadPinned(other) = %v, %v", b, err)
	}
	if b, err := load(cache, pin, raw, &fetches); err != nil || b.SHA256 != pin {
		t.Fatalf("LoadPinned(pin) = %v, %v", b, err)
	}
	if n := fetches.Load(); n != 3 {
		t.Fatalf("fetches = %d, want 3 (mismatch, other, pin)", n)
	}
	if b, err := load(cache, pin, other, &fetches); err != nil || b.SHA256 != pin || fetches.Load() != 3 {
		t.Fatalf("a cached pin must be served from the cache: %v, %v", b, err)
	}
}

func TestCache_evicts_least_recently_used_past_the_byte_cap(t *testing.T) {
	ra, a := cacheRaw(t, strings.Repeat("a", 40))
	rb, b := cacheRaw(t, strings.Repeat("b", 40))
	rc, c := cacheRaw(t, strings.Repeat("c", 40))
	cache := NewCache(100)
	var fetches atomic.Int32
	mustLoad := func(pin string, raw []byte) {
		t.Helper()
		if _, err := load(cache, pin, raw, &fetches); err != nil {
			t.Fatal(err)
		}
	}
	mustLoad(a, ra)
	mustLoad(b, rb)
	mustLoad(a, ra) // a is now the most recent (cached: no fetch)
	mustLoad(c, rc) // 120 bytes > 100: evicts b (least recently used)
	if n := fetches.Load(); n != 3 {
		t.Fatalf("fetches = %d, want 3", n)
	}
	mustLoad(a, ra)
	mustLoad(c, rc)
	if n := fetches.Load(); n != 3 {
		t.Fatalf("a and c must stay cached (fetches = %d)", n)
	}
	mustLoad(b, rb)
	if n := fetches.Load(); n != 4 {
		t.Fatalf("b should have been evicted (fetches = %d)", n)
	}
}

func TestCache_keeps_one_oversized_bundle(t *testing.T) {
	raw, big := cacheRaw(t, strings.Repeat("x", 500))
	cache := NewCache(100)
	var fetches atomic.Int32
	for i := 0; i < 2; i++ {
		if _, err := load(cache, big, raw, &fetches); err != nil {
			t.Fatal(err)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Fatal("the most recent bundle must stay cached")
	}
}

func TestCache_LoadPinned_shares_one_load_and_does_not_cache_errors(t *testing.T) {
	raw, pin := cacheRaw(t, "<h1>hi</h1>")
	cache := NewCache(1 << 20)
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cache.LoadPinned(pin, SiteLimits, func() ([]byte, error) {
				calls.Add(1)
				<-release
				return raw, nil
			})
			if err != nil || got.SHA256 != pin {
				t.Errorf("LoadPinned = %v, %v", got, err)
			}
		}()
	}
	for calls.Load() == 0 {
	}
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("load ran %d times", n)
	}

	raw2, pin2 := cacheRaw(t, "<h1>other</h1>")
	boom := errors.New("boom")
	if _, err := cache.LoadPinned(pin2, SiteLimits, func() ([]byte, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := load(cache, pin2, raw2, nil); err != nil {
		t.Fatal("a failed load must not be cached")
	}
}
