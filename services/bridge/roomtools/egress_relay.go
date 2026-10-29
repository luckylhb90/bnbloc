package roomtools

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/component"
)

// A relayed send (component.EgressSend.Relay,
// docs/prd/2026-10-27-model-credentials.md §3.4): a model call with the
// member's credential. Unlike a send whose first 64 KiB are held back for
// the component's report, its head goes to the command as soon as the system
// answers and each chunk follows as it arrives (a completion arrives at a
// few hundred bytes a second); the component hears of it once, when it
// ended. The credential the send set is removed from the answer's headers
// and, for an error, from its body.

// relayIdle is a relayed send's idle window.
func relayIdle(send *component.EgressSend) time.Duration {
	n := send.RelayIdleSeconds
	if n <= 0 {
		n = component.DefaultEgressRelayIdle
	}
	return time.Duration(min(n, component.MaxEgressRelayIdle)) * time.Second
}

// credentialHeader reports a header or query name that carries a credential.
func credentialHeader(name string) bool {
	n := strings.ToLower(name)
	return n == "authorization" || n == "proxy-authorization" || n == "cookie" ||
		strings.Contains(n, "key") || strings.Contains(n, "token") || strings.Contains(n, "secret") || strings.Contains(n, "auth")
}

// minScrub is the shortest value scrubbed: a shorter one would mangle
// ordinary text and is not a credential.
const minScrub = 8

// scrubValues are the exact strings the answer must not carry back: what
// the send named (Scrub), the values of the headers and query parameters it
// set under a credential's name, and the token after a scheme
// ("Bearer <token>").
func scrubValues(send *component.EgressSend) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if len(v) >= minScrub && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v := range send.Scrub {
		add(v)
	}
	value := func(v string) {
		add(v)
		if i := strings.LastIndexByte(v, ' '); i > 0 {
			add(v[i+1:])
		}
	}
	for k, vs := range send.Header {
		if credentialHeader(k) {
			for _, v := range vs {
				value(v)
			}
		}
	}
	for k, vs := range send.Query {
		if credentialHeader(k) {
			for _, v := range vs {
				value(v)
			}
		}
	}
	return out
}

// scrub removes every value from b.
func scrub(b []byte, values []string) []byte {
	for _, v := range values {
		b = bytes.ReplaceAll(b, []byte(v), []byte("[removed]"))
	}
	return b
}

// relayAnswer turns a system's answer into the command's: the head now, the body
// as it arrives. cancel ends the request's context (the body's Close).
func relayAnswer(resp *http.Response, cancel context.CancelFunc, in component.EgressRequest, send *component.EgressSend, h exchangeHooks) *http.Response {
	values := scrubValues(send)
	out := &http.Response{StatusCode: resp.StatusCode, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, ContentLength: resp.ContentLength}
	for k, vs := range resp.Header {
		out.Header[k] = append([]string(nil), vs...)
	}
	for _, hd := range append(hopHeaders, "Content-Length", "Set-Cookie") {
		out.Header.Del(hd)
	}
	for k, vs := range out.Header {
		for i, v := range vs {
			out.Header[k][i] = string(scrub([]byte(v), values))
		}
	}
	start := time.Now()
	rb := &relayUsage{resp: resp, cancel: cancel, in: in, h: h, start: start}
	var body io.Reader = resp.Body
	if resp.StatusCode >= 400 {
		// An error body is buffered (at most 64 KiB) and scrubbed whole: a
		// provider that echoes the key must not hand it to the command.
		raw, err := io.ReadAll(io.LimitReader(resp.Body, component.MaxEgressReportBody))
		if err != nil {
			rb.err = err
		}
		raw = scrub(raw, values)
		body, out.ContentLength = bytes.NewReader(raw), int64(len(raw))
		out.Header.Del("Content-Encoding")
	}
	if out.ContentLength < 0 {
		out.TransferEncoding = []string{"chunked"}
	}
	rb.r = body
	out.Body = rb
	if in.Method == http.MethodHead {
		_ = out.Body.Close()
		out.Body, out.ContentLength = http.NoBody, 0
	}
	return out
}

// relayUsage is the answer's body on its way to the command, counting what
// went and telling the component once when it ended.
type relayUsage struct {
	r      io.Reader
	resp   *http.Response
	cancel context.CancelFunc
	in     component.EgressRequest
	h      exchangeHooks
	start  time.Time
	err    error

	once sync.Once
	mu   sync.Mutex // the counters: read by the goroutine that closes
	// bytes, eof and err are what Read saw.
	bytes int64
	eof   bool
}

func (b *relayUsage) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.mu.Lock()
	b.bytes += int64(n)
	switch {
	case err == io.EOF:
		b.eof = true
	case err != nil:
		b.err = err
	}
	b.mu.Unlock()
	return n, err
}

// abort ends the send now (the command went away): a read waiting on the
// system returns, and Close then reports.
func (b *relayUsage) abort() {
	b.cancel()
	_ = b.resp.Body.Close()
}

func (b *relayUsage) Close() error {
	err := b.resp.Body.Close()
	b.once.Do(func() {
		b.cancel()
		if b.h.onUsage == nil {
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		u := component.EgressUsage{Method: b.in.Method, Status: b.resp.StatusCode, Bytes: b.bytes, Millis: time.Since(b.start).Milliseconds()}
		if i := strings.Index(b.in.URL, "://"); i >= 0 {
			u.Host = strings.SplitN(b.in.URL[i+3:], "/", 2)[0]
		}
		switch {
		case b.err != nil:
			u.Error = "the answer stopped: " + b.err.Error()
		case !b.eof:
			u.Error = "the command dropped the answer"
		}
		b.h.onUsage(u)
	})
	return err
}
