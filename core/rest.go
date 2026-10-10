package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultRequestTimeout bounds one core HTTP request when REST.Timeout is
// unset. Core daemons are local and answer in milliseconds; the longest
// legitimate single request is a session create that waits for a runner to
// come online (tens of seconds). Without a bound a hung daemon pins the
// caller forever (a bridge reflow keeps renewing its job lease while nothing
// happens). It is applied as a per-request context deadline, never as
// http.Client.Timeout, so a caller that needs a tighter (poll) or longer
// budget passes its own context deadline: the earlier deadline always wins.
const DefaultRequestTimeout = 2 * time.Minute

// REST is the shared bearer+JSON HTTP client every core provider that speaks a
// plain REST daemon uses, so a new provider does not hand-copy the same
// request/response plumbing (see AGENTS.md "Reuse before you add"). It is
// stateless: the caller passes the member's Endpoint per request, matching the
// Provider contract. Name identifies the core in error messages.
type REST struct {
	// Name prefixes errors, e.g. "acme". Required for legible failures.
	Name string
	// HTTP is the client to use; nil falls back to http.DefaultClient.
	HTTP *http.Client
	// Timeout bounds each request; zero uses DefaultRequestTimeout.
	Timeout time.Duration
}

type HTTPError struct {
	Core   string
	Method string
	Path   string
	Status int
	Body   []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s %s: status %d: %s", e.Core, e.Method, e.Path, e.Status, Truncate(e.Body, 256))
}

func (c REST) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Get issues a GET against ep.BaseURL+path with the endpoint's bearer token.
func (c REST) Get(ctx context.Context, ep Endpoint, path string) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, ep, path, nil)
}

// Post issues a POST with a JSON-encoded body (nil body sends no payload).
func (c REST) Post(ctx context.Context, ep Endpoint, path string, body any) ([]byte, error) {
	return c.Do(ctx, http.MethodPost, ep, path, body)
}

// Do performs one request against the endpoint. It JSON-encodes body, sets the
// bearer token when present, and returns the response bytes — or a status-aware
// error carrying the core name, method, path, and a truncated body snippet.
func (c REST) Do(ctx context.Context, method string, ep Endpoint, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	return c.DoRaw(ctx, method, ep, path, reader, body != nil)
}

// DoRaw is Do for a caller that renders its own request body (e.g. a manifest
// template): jsonBody marks the body as application/json. The response
// handling, bearer, timeout, and error shape are shared with Do so every core
// client speaks to a daemon the same way.
func (c REST) DoRaw(ctx context.Context, method string, ep Endpoint, path string, reader io.Reader, jsonBody bool) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(ep.BaseURL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	if jsonBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if ep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Token)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		// A body cut short (deadline, reset) is a failed request, not a short
		// success: decoding a truncated document would be silently wrong.
		return nil, fmt.Errorf("%s %s %s: read response: %w", c.Name, method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{Core: c.Name, Method: method, Path: path, Status: resp.StatusCode, Body: raw}
	}
	return raw, nil
}

// ShortID returns a short random hex id for workspace/session names.
func ShortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// UUIDv4 returns a random RFC-4122 v4 UUID string.
func UUIDv4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Truncate renders up to n bytes of b for error messages.
func Truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
