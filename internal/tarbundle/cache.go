package tarbundle

import (
	"container/list"
	"sync"
)

// Cache keeps parsed bundles by archive sha256, least recently used first
// out once their file bytes exceed the cap. Bundles are content-addressed,
// so an entry is never stale. Concurrent loads of one digest share a single
// load. The zero value is not usable; use NewCache.
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

// Get returns the cached bundle with this archive sha256.
func (c *Cache) Get(sha string) (*Bundle, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[sha]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*Bundle), true
}

// GetOrLoad returns the bundle with archive sha256 sha, calling load once
// (across concurrent callers) on a miss and caching its result. load must
// return a bundle whose SHA256 is sha (Parse with the pinned digest).
func (c *Cache) GetOrLoad(sha string, load func() (*Bundle, error)) (*Bundle, error) {
	c.mu.Lock()
	if el, ok := c.byKey[sha]; ok {
		c.order.MoveToFront(el)
		c.mu.Unlock()
		return el.Value.(*Bundle), nil
	}
	if l, ok := c.loading[sha]; ok {
		c.mu.Unlock()
		<-l.done
		return l.b, l.err
	}
	l := &cacheLoad{done: make(chan struct{})}
	c.loading[sha] = l
	c.mu.Unlock()

	l.b, l.err = load()
	c.mu.Lock()
	delete(c.loading, sha)
	if l.err == nil {
		c.putLocked(l.b)
	}
	c.mu.Unlock()
	close(l.done)
	return l.b, l.err
}

// Put caches b under its SHA256.
func (c *Cache) Put(b *Bundle) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(b)
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
