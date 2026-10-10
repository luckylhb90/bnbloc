package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
)

// RetryHinter is implemented by errors that carry the daemon's own verdict on
// whether repeating the request is safe (the Core Protocol error envelope's
// "retryable" field). The daemon's verdict always wins over local heuristics.
type RetryHinter interface {
	RetryHint() bool
}

// Retryable reports whether a failed core request may be repeated.
//
// idempotent marks a request whose repetition cannot duplicate an effect
// (reads and polls). A non-idempotent request — creating a session or sending
// input, both of which start agent work — is repeated only when it provably
// never took effect: the connection was never established (dial failure), the
// daemon answered 429/503 (not processed), or the daemon itself marked the
// error retryable. An ambiguous failure (reset/EOF mid-response, a timeout, a
// 500/502/504) may mean the daemon already ran the turn, so retrying it would
// run the prompt twice; those are retried only for idempotent requests.
func Retryable(err error, idempotent bool) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var hint RetryHinter
	if errors.As(err, &hint) {
		return hint.RetryHint()
	}
	var he *HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			return true
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout:
			return idempotent
		default:
			return false
		}
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	if !idempotent {
		return false
	}
	var ne net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout())
}
