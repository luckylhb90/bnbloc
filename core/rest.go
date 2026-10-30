package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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

// MaxResponseBytes bounds a buffered response body (a JSON document, a
// session file read). A larger answer is a failed request, never a
// truncated success.
const MaxResponseBytes = 64 << 20

// maxErrorBodyBytes bounds what a failed response keeps of its body: enough
// for any error envelope and a legible message.
const maxErrorBodyBytes = 64 << 10

// REST is the one HTTP plumbing every bnbloc protocol client goes through: a
// manifest core, the Core Protocol client and the Sandbox Protocol client
// (see AGENTS.md "Reuse before you add"). Request is the one place the
// endpoint's bearer is set, Send the one bounded read and error mapping of a
// failed answer, JSON the one decode. It is stateless: the caller passes the
// Endpoint per request, matching the Provider contract.
type REST struct {
	// Name prefixes errors, e.g. "acme". Required for legible failures.
	Name string
	// HTTP is the client to use; nil falls back to http.DefaultClient.
	HTTP *http.Client
	// Timeout bounds each buffered request (Do, DoRaw, JSON); zero uses
	// DefaultRequestTimeout. Send (a stream) is bounded by its context only.
	Timeout time.Duration
	// DecodeError maps a failed answer onto the protocol's typed error (a
	// protocol client sets DecodeEnvelope); nil keeps the *HTTPError.
	DecodeError func(*HTTPError) error
}

// HTTPError is a non-2xx answer: the request and the (bounded) body.
type HTTPError struct {
	Core   string
	Method string
	Path   string
	Status int
	Body   []byte
}

// MaxErrorBodyBytes is how much of a core's error body an error message keeps. An agent's own
// answer (its auth methods, a stack's first lines) is inside it and is what tells a member or an
// operator why a session did not start: 256 cut it before the cause.
const MaxErrorBodyBytes = 1024

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s %s: status %d: %s", e.Core, e.Method, e.Path, e.Status, Truncate(e.Body, MaxErrorBodyBytes))
}

// NotFound reports a core's 404 answer: what the request named (a
// workspace, a session, a target, a file) does not exist there, so the
// request took no effect. The bridge reads it to tell a workspace the core
// no longer holds from any other failure of a session create.
func NotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// EnvelopeError is a failed answer that carried a protocol error envelope:
// Err is the protocol's typed error, HTTP the answer it arrived in.
// errors.As reaches both, so a caller branches on the typed code while
// Retryable still sees the status when the envelope states no retry hint.
type EnvelopeError struct {
	Err  error
	HTTP *HTTPError
}

func (e *EnvelopeError) Error() string   { return e.Err.Error() }
func (e *EnvelopeError) Unwrap() []error { return []error{e.Err, e.HTTP} }

// DecodeEnvelope is the REST.DecodeError of a protocol whose failed answers
// carry {"error": T} with a non-empty "code" (Core Protocol, Sandbox
// Protocol): each protocol keeps its own error type and code set, the
// decoding is shared. A body that is not such an envelope stays the
// *HTTPError.
func DecodeEnvelope[T any, PT interface {
	*T
	error
}]() func(*HTTPError) error {
	return func(he *HTTPError) error {
		var probe struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(he.Body, &probe) != nil || probe.Error.Code == "" {
			return he
		}
		var env struct {
			Error T `json:"error"`
		}
		if json.Unmarshal(he.Body, &env) != nil {
			return he
		}
		return &EnvelopeError{Err: PT(&env.Error), HTTP: he}
	}
}

func (c REST) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Request builds one request against ep: ep.BaseURL+path, the endpoint's
// bearer, and contentType when set. It is the only place a bnbloc protocol
// client puts a token on the wire.
func (c REST) Request(ctx context.Context, method string, ep Endpoint, path string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(ep.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if ep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Token)
	}
	return req, nil
}

// Send performs req and returns a 2xx response for the caller to read and
// close. Any other answer is read (bounded), closed and returned as an
// error: the *HTTPError, mapped through DecodeError. Send adds no deadline
// beyond req's context, so a stream (an exec, an archive) may outlive any
// fixed bound.
func (c REST) Send(req *http.Request) (*http.Response, error) {
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	he := &HTTPError{Core: c.Name, Method: req.Method, Path: req.URL.RequestURI(), Status: resp.StatusCode, Body: raw}
	if c.DecodeError != nil {
		return nil, c.DecodeError(he)
	}
	return nil, he
}

// Get issues a GET against ep.BaseURL+path with the endpoint's bearer token.
func (c REST) Get(ctx context.Context, ep Endpoint, path string) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, ep, path, nil)
}

// Do performs one buffered request against the endpoint: body is
// JSON-encoded (nil sends none) and the answer's bytes are returned, or the
// Send error.
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

// JSON is Do plus decoding the answer into out (a nil out, or an empty
// answer, decodes nothing).
func (c REST) JSON(ctx context.Context, method string, ep Endpoint, path string, body, out any) error {
	raw, err := c.Do(ctx, method, ep, path, body)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: decode %s %s: %w", c.Name, method, path, err)
	}
	return nil
}

// DoRaw is Do for a caller that renders its own request body (e.g. a manifest
// template): jsonBody marks the body as application/json. The request is
// bounded by Timeout and its answer by MaxResponseBytes.
func (c REST) DoRaw(ctx context.Context, method string, ep Endpoint, path string, reader io.Reader, jsonBody bool) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	contentType := ""
	if jsonBody {
		contentType = "application/json"
	}
	req, err := c.Request(ctx, method, ep, path, reader, contentType)
	if err != nil {
		return nil, err
	}
	resp, err := c.Send(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		// A body cut short (deadline, reset) is a failed request, not a short
		// success: decoding a truncated document would be silently wrong.
		return nil, fmt.Errorf("%s %s %s: read response: %w", c.Name, method, path, err)
	}
	if len(raw) > MaxResponseBytes {
		return nil, fmt.Errorf("%s %s %s: response exceeds %d bytes", c.Name, method, path, MaxResponseBytes)
	}
	return raw, nil
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
