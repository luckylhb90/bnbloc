package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type hinted struct{ retry, stated bool }

func (h hinted) Error() string                   { return "hinted" }
func (h hinted) RetryHint() (retry, stated bool) { return h.retry, h.stated }

// A non-idempotent request (create session / send input) is repeated only when
// it provably never took effect; an ambiguous failure could run the prompt
// twice (BR-13).
func TestRetryable(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	cases := []struct {
		name       string
		err        error
		idempotent bool
		want       bool
	}{
		{"nil", nil, true, false},
		{"canceled", context.Canceled, true, false},
		{"daemon says retryable", fmt.Errorf("create: %w", hinted{true, true}), false, true},
		{"daemon says not retryable", fmt.Errorf("create: %w", hinted{false, true}), true, false},
		{"no verdict, 503 envelope", &EnvelopeError{Err: hinted{}, HTTP: &HTTPError{Status: http.StatusServiceUnavailable}}, false, true},
		{"no verdict, 502 envelope", &EnvelopeError{Err: hinted{}, HTTP: &HTTPError{Status: http.StatusBadGateway}}, false, false},
		{"not retryable, 503 envelope", &EnvelopeError{Err: hinted{false, true}, HTTP: &HTTPError{Status: http.StatusServiceUnavailable}}, false, false},
		{"503 not processed", &HTTPError{Status: http.StatusServiceUnavailable}, false, true},
		{"429 not processed", &HTTPError{Status: http.StatusTooManyRequests}, false, true},
		{"502 ambiguous, non-idempotent", &HTTPError{Status: http.StatusBadGateway}, false, false},
		{"502 ambiguous, idempotent", &HTTPError{Status: http.StatusBadGateway}, true, true},
		{"400 never", &HTTPError{Status: http.StatusBadRequest}, true, false},
		{"dial failure", fmt.Errorf("post: %w", dial), false, true},
		{"EOF ambiguous, non-idempotent", io.EOF, false, false},
		{"EOF idempotent", io.ErrUnexpectedEOF, true, true},
		{"deadline idempotent", context.DeadlineExceeded, true, true},
		{"deadline non-idempotent", context.DeadlineExceeded, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Retryable(c.err, c.idempotent); got != c.want {
				t.Fatalf("Retryable(%v, %v) = %v, want %v", c.err, c.idempotent, got, c.want)
			}
		})
	}
}

// A hung core must not pin the caller forever (BR-3): every REST request is
// bounded by a per-request deadline, and a tighter caller deadline still wins.
func TestRESTRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ep := Endpoint{BaseURL: srv.URL}

	start := time.Now()
	_, err := REST{Name: "hung", Timeout: 100 * time.Millisecond}.Get(context.Background(), ep, "/x")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("request was not bounded by REST.Timeout")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := (REST{Name: "hung"}).Get(ctx, ep, "/x"); err == nil {
		t.Fatal("caller deadline must still bound the request")
	}
}
