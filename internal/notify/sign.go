// Package notify produces member notifications from room activity and
// delivers them to the installed notifier components (endpoint components of
// kind bnbloc.notifier.package/v1). Producers (the realtime publish tap, and
// any service through Emitter) filter by member preferences and enqueue into
// a durable outbox, one item per (event, notifier); a Worker drains the
// outbox with signed HTTP POSTs and exponential backoff. Nothing here runs on the
// chat publish path: observation is a non-blocking hand-off to a bounded
// queue, and every later step is best-effort.
package notify

import (
	"time"

	"github.com/luckylhb90/bnbloc/component"
)

// SignatureTolerance is the receiver-side timestamp window the contract
// recommends (docs/notifications.md).
const SignatureTolerance = component.SignatureTolerance

// Sign returns the X-Bnbloc-Signature value for body at time t:
// "t=<unix>,v1=<hex(HMAC-SHA256(secret, "<unix>." + body))>". The scheme
// is the component contract's (component.Sign).
func Sign(secret string, t time.Time, body []byte) string {
	return component.Sign(secret, t, body)
}

// Signature verification errors.
var (
	ErrSignatureMalformed = component.ErrSignatureMalformed
	ErrSignatureMismatch  = component.ErrSignatureMismatch
	ErrSignatureExpired   = component.ErrSignatureExpired
)

// Verify checks a signature header against body (component.Verify).
func Verify(header, secret string, body []byte, now time.Time, tolerance time.Duration) error {
	return component.Verify(header, secret, body, now, tolerance)
}
