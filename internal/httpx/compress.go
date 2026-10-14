package httpx

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Compressed responses for static bytes: the web client's assets and
// previewer bundle files. A deployment's edge may not compress (one QA
// ingress served a 1.2 MB previewer script raw), and those are the largest
// things a browser downloads, so the gateway compresses them itself. Their
// bytes never change under a given key (a content-hashed asset name, a
// bundle sha and path), so each is compressed once and kept, up to
// maxCompressedBytes in all (the oldest go first).

const (
	// Below this a gzip header and dictionary cost more than they save.
	minCompressBytes   = 1024
	maxCompressedBytes = 64 << 20
)

// Compressible reports whether a content type is text worth compressing.
func Compressible(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch {
	case strings.HasPrefix(ct, "text/"):
		return true
	case ct == "application/javascript", ct == "application/json", ct == "application/manifest+json",
		ct == "image/svg+xml", ct == "application/xml", ct == "application/wasm":
		return true
	}
	return false
}

type gzCache struct {
	mu    sync.Mutex
	items map[string][]byte
	order []string
	size  int
}

var compressed = &gzCache{items: map[string][]byte{}}

func (c *gzCache) get(key string, data []byte) []byte {
	c.mu.Lock()
	if b, ok := c.items[key]; ok {
		c.mu.Unlock()
		return b
	}
	c.mu.Unlock()
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(data)
	_ = zw.Close()
	out := buf.Bytes()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; !ok {
		c.items[key] = out
		c.order = append(c.order, key)
		c.size += len(out)
		for c.size > maxCompressedBytes && len(c.order) > 1 {
			old := c.order[0]
			c.order = c.order[1:]
			c.size -= len(c.items[old])
			delete(c.items, old)
		}
	}
	return c.items[key]
}

// WriteStatic writes data, gzip-compressed when the client accepts it and
// the Content-Type already set on w is compressible. key must name these
// exact bytes for as long as the process runs (a content hash and path).
// Content-Length is set either way; HEAD writes no body.
func WriteStatic(w http.ResponseWriter, r *http.Request, key string, data []byte) {
	h := w.Header()
	h.Add("Vary", "Accept-Encoding")
	body := data
	if len(data) >= minCompressBytes && Compressible(h.Get("Content-Type")) && acceptsGzip(r) {
		body = compressed.get(key, data)
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if enc == "gzip" && !strings.Contains(strings.ReplaceAll(part, " ", ""), "q=0") {
			return true
		}
	}
	return false
}
