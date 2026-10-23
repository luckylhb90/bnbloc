package component

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SignatureHeader carries the signature of a call between the platform and
// an endpoint component, in either direction:
// "t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<unix>." + signed)>".
// signed is the call's body, or what a call without one signs in its place
// (SignedRequest). The secret is the component's credential.
const SignatureHeader = "X-Bnbloc-Signature"

// SignatureTolerance is the receiver-side timestamp window.
const SignatureTolerance = 5 * time.Minute

// Signature verification errors.
var (
	ErrSignatureMalformed = errors.New("malformed signature header")
	ErrSignatureMismatch  = errors.New("signature mismatch")
	ErrSignatureExpired   = errors.New("signature timestamp outside tolerance")
)

// Sign returns the SignatureHeader value for signed at time t.
func Sign(secret string, t time.Time, signed []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + hex.EncodeToString(signatureMAC(secret, ts, signed))
}

func signatureMAC(secret, ts string, signed []byte) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ts))
	h.Write([]byte("."))
	h.Write(signed)
	return h.Sum(nil)
}

// Verify checks a SignatureHeader value against signed: the HMAC must match
// (any of several v1 values, so a receiver tolerates a multi-signature
// header) and the timestamp must be within tolerance of now.
func Verify(header, secret string, signed []byte, now time.Time, tolerance time.Duration) error {
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
	want := signatureMAC(secret, ts, signed)
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

// SignedRequest is what a call between the platform and a service
// component signs: its method, its path with the query, the identity it
// carries (the TurnHeader or MemberHeader value, "" when none) and its
// body, so no part of one call can be replayed in another.
func SignedRequest(method, pathQuery, identity string, body []byte) []byte {
	out := make([]byte, 0, len(method)+len(pathQuery)+len(identity)+len(body)+3)
	out = append(out, method...)
	out = append(out, ' ')
	out = append(out, pathQuery...)
	out = append(out, '\n')
	out = append(out, identity...)
	out = append(out, '\n')
	return append(out, body...)
}
