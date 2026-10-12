package tarbundle

import (
	"container/list"
	"errors"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/digest"
)

// Cache keeps parsed bundles by archive sha256, least recently used first
// out once their file bytes exceed the cap. Bundles are content-addressed,
// so an entry is never stale. Concurrent loads of one digest share a single
// load. The zero value is not usable; use NewCache.
//
// LoadPinned is the only way in or out: every cached bundle was verified
// against the pin it is looked up by (TestBundleLoadsArePinned).
type Cache struct {
	maxBytes int

	mu      sync.Mutex
	order   *list.List // front = most recently used; values are *Bundle
	byKey   map[string]*list.Element
	bytes   int
	loading map[string]*cacheLoad
}

type cacheLoad struct {
	done chan struct{}
	b    *Bundle
	err  error
}

// NewCache returns a cache holding at most maxBytes of bundle file bytes
// (the most recent bundle is always kept, even when larger).
func NewCache(maxBytes int) *Cache {
	return &Cache{maxBytes: maxBytes, order: list.New(), byKey: map[string]*list.Element{}, loading: map[string]*cacheLoad{}}
}

// ErrUnpinned refuses a bundle load without a canonical sha256 pin.
var ErrUnpinned = errors.New("bundle load needs its sha256 pin")

// LoadPinned returns the bundle whose archive sha256 is sha: from the cache,
// or fetched once (across concurrent callers), parsed within l and verified
// against sha. An empty or non-canonical pin is refused before anything is
// fetched; bytes that do not match the pin fail with ErrDigest and are never
// cached. A failed load is not cached either.
func (c *Cache) LoadPinned(sha string, l Limits, fetch func() ([]byte, error)) (*Bundle, error) {
	if !digest.Valid(sha) {
		return nil, ErrUnpinned
	}
	c.mu.Lock()
	if el, ok := c.byKey[sha]; ok {
		c.order.MoveToFront(el)
		c.mu.Unlock()
		return el.Value.(*Bundle), nil
	}
	if ld, ok := c.loading[sha]; ok {
		c.mu.Unlock()
		<-ld.done
		return ld.b, ld.err
	}
	ld := &cacheLoad{done: make(chan struct{})}
	c.loading[sha] = ld
	c.mu.Unlock()

	if raw, err := fetch(); err != nil {
		ld.err = err
	} else {
		ld.b, ld.err = Parse(raw, sha, l)
	}
	c.mu.Lock()
	delete(c.loading, sha)
	if ld.err == nil {
		c.putLocked(ld.b)
	}
	c.mu.Unlock()
	close(ld.done)
	return ld.b, ld.err
}

func (c *Cache) putLocked(b *Bundle) {
	if el, ok := c.byKey[b.SHA256]; ok {
		c.order.MoveToFront(el)
		return
	}
	c.byKey[b.SHA256] = c.order.PushFront(b)
	c.bytes += b.Size()
	for c.bytes > c.maxBytes && c.order.Len() > 1 {
		oldest := c.order.Back()
		ob := oldest.Value.(*Bundle)
		c.order.Remove(oldest)
		delete(c.byKey, ob.SHA256)
		c.bytes -= ob.Size()
	}
}
