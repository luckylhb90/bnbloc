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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SignatureTolerance is the receiver-side timestamp window the contract
// recommends (docs/notifications.md).
const SignatureTolerance = 5 * time.Minute

// Sign returns the X-Bnbloc-Signature value for body at time t:
// "t=<unix>,v1=<hex(HMAC-SHA256(secret, "<unix>." + body))>".
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac(secret, ts, body))
}

func mac(secret, ts string, body []byte) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ts))
	h.Write([]byte("."))
	h.Write(body)
	return h.Sum(nil)
}

// Signature verification errors.
var (
	ErrSignatureMalformed = errors.New("notify: malformed signature header")
	ErrSignatureMismatch  = errors.New("notify: signature mismatch")
	ErrSignatureExpired   = errors.New("notify: signature timestamp outside tolerance")
)

// Verify checks a signature header against body: the HMAC must match (any of
// several v1 values, so a receiver tolerates a future multi-signature header)
// and the timestamp must be within tolerance of now. It is the reference
// implementation of the receiver side of the contract.
func Verify(header, secret string, body []byte, now time.Time, tolerance time.Duration) error {
	var ts string
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			if raw, err := hex.DecodeString(v); err == nil {
				sigs = append(sigs, raw)
			}
		}
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return ErrSignatureMalformed
	}
	want := mac(secret, ts, body)
	matched := false
	for _, s := range sigs {
		if hmac.Equal(s, want) {
			matched = true
		}
	}
	if !matched {
		return ErrSignatureMismatch
	}
	if d := now.Sub(time.Unix(unix, 0)); d > tolerance || d < -tolerance {
		return ErrSignatureExpired
	}
	return nil
}
