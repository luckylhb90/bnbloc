package tarbundle

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func cacheBundle(t *testing.T, body string) *Bundle {
	t.Helper()
	raw, err := PackFiles(map[string][]byte{"index.html": []byte(body)}, SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(raw, "", SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCache_evicts_least_recently_used_past_the_byte_cap(t *testing.T) {
	a, b, c := cacheBundle(t, strings.Repeat("a", 40)), cacheBundle(t, strings.Repeat("b", 40)), cacheBundle(t, strings.Repeat("c", 40))
	cache := NewCache(100)
	cache.Put(a)
	cache.Put(b)
	if _, ok := cache.Get(a.SHA256); !ok { // a is now the most recent
		t.Fatal("a missing")
	}
	cache.Put(c) // 120 bytes > 100: evicts b (least recently used)

	if _, ok := cache.Get(b.SHA256); ok {
		t.Fatal("b should have been evicted")
	}
	for _, keep := range []*Bundle{a, c} {
		if _, ok := cache.Get(keep.SHA256); !ok {
			t.Fatalf("%s evicted", keep.SHA256)
		}
	}
}

func TestCache_keeps_one_oversized_bundle(t *testing.T) {
	big := cacheBundle(t, strings.Repeat("x", 500))
	cache := NewCache(100)
	cache.Put(big)
	if _, ok := cache.Get(big.SHA256); !ok {
		t.Fatal("the most recent bundle must stay cached")
	}
}

func TestCache_GetOrLoad_shares_one_load_and_does_not_cache_errors(t *testing.T) {
	b := cacheBundle(t, "<h1>hi</h1>")
	cache := NewCache(1 << 20)
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cache.GetOrLoad(b.SHA256, func() (*Bundle, error) {
				calls.Add(1)
				<-release
				return b, nil
			})
			if err != nil || got != b {
				t.Errorf("GetOrLoad = %v, %v", got, err)
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

	boom := errors.New("boom")
	if _, err := cache.GetOrLoad("other", func() (*Bundle, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := cache.GetOrLoad("other", func() (*Bundle, error) { return b, nil }); err != nil {
		t.Fatal("a failed load must not be cached")
	}
}
