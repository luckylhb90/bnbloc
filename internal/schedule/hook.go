package schedule

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A task's webhook (docs/prd/2026-10-21-room-events.md §2.3): a trigger
// subscribing to wire.SubscribeWebhook gets its own URL
// (wire.HookPathPrefix + its hook id) and a secret shown once. This file
// owns the scheme: the id and secret (NewHook), the signature
// (VerifyHookSignature: HMAC-SHA256 of the raw body, GitHub's scheme, in
// wire.HookSignatureHeader or wire.GitHubSignatureHeader) and the event a
// request carries (HookEvent). realtime's hook route
// (services/realtime/hooks.go) applies it.

// MaxHookBodyBytes bounds a webhook request's body. It is above the event's
// data bound (wire.MaxEventDataBytes) because a repository host's payload
// is often larger: HookEvent keeps what fits and names what it left out.
const MaxHookBodyBytes = 512 << 10

// hookSecretBytes is the secret's randomness.
const hookSecretBytes = 32

// hookSecretPrefix opens every webhook secret, so a leaked one is
// recognizable.
const hookSecretPrefix = "whsec_"

// NewHookID mints a webhook's id (the URL's last segment). It is not the
// credential: the secret is.
func NewHookID() string { return ids.New("hk_") }

// NewHookSecret mints a webhook secret (fully random).
func NewHookSecret() (string, error) {
	b := make([]byte, hookSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hookSecretPrefix + hex.EncodeToString(b), nil
}

// HookSecretAD is the additional data a hook's secret is sealed under
// (sealbox), so a sealed secret copied to another hook does not open.
func HookSecretAD(hookID string) string { return "schedule.hook/" + hookID }

// VerifyHookSignature reports whether the request is signed with secret:
// "sha256=<hex HMAC-SHA256(secret, body)>" in wire.HookSignatureHeader or
// wire.GitHubSignatureHeader, compared in constant time. An unsigned
// request, or one signed another way, is not.
func VerifyHookSignature(h http.Header, secret string, body []byte) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, name := range []string{wire.HookSignatureHeader, wire.GitHubSignatureHeader} {
		v, ok := strings.CutPrefix(strings.TrimSpace(h.Get(name)), "sha256=")
		if !ok {
			continue
		}
		if got, err := hex.DecodeString(v); err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// SignHookBody is the signature header value of body under secret (what an
// outside system sends; tests and the CLI's docs use it).
func SignHookBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ErrHookNotJSON refuses a webhook body that is not one JSON value.
var ErrHookNotJSON = errors.New("the body is not JSON")

// HookEvent is the event a verified webhook request to hookID carries, at
// now. Its source is the hook's (wire.SourceWebhookPrefix + hookID,
// whatever the body says). Its type is wire.HookEventHeader, else
// "com.github." + wire.GitHubEventHeader, else the body's top-level
// "type"; its id the delivery header, else the body's "id", else a new
// one; its subject the body's "subject" when it is a string. A body that
// is a CloudEvent already (a top-level "specversion") gives its own
// attributes and its "data". Data over wire.MaxEventDataBytes keeps the
// fields that fit, largest left out first, and names the rest in
// "_omitted" (never silently).
func HookEvent(hookID string, h http.Header, body []byte, now time.Time) (wire.CloudEvent, error) {
	if !json.Valid(body) {
		return wire.CloudEvent{}, ErrHookNotJSON
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(body, &top) // nil unless the body is an object
	str := func(k string) string {
		var s string
		if json.Unmarshal(top[k], &s) != nil {
			return ""
		}
		return strings.TrimSpace(s)
	}
	ev := wire.CloudEvent{Source: wire.SourceWebhookPrefix + hookID, Time: wire.EventTime(now), Data: body}
	if _, structured := top["specversion"]; structured {
		ev.Type, ev.ID, ev.Subject, ev.Data = str("type"), str("id"), str("subject"), top["data"]
		if t := str("time"); t != "" {
			if at, err := time.Parse(time.RFC3339Nano, t); err == nil {
				ev.Time = wire.EventTime(at)
			}
		}
	} else {
		ev.ID, ev.Subject = str("id"), str("subject")
		ev.Type = str("type")
	}
	switch {
	case strings.TrimSpace(h.Get(wire.HookEventHeader)) != "":
		ev.Type = strings.TrimSpace(h.Get(wire.HookEventHeader))
	case strings.TrimSpace(h.Get(wire.GitHubEventHeader)) != "":
		ev.Type = "com.github." + strings.TrimSpace(h.Get(wire.GitHubEventHeader))
	}
	if ev.Type == "" {
		return wire.CloudEvent{}, errNoType
	}
	for _, name := range []string{wire.HookDeliveryHeader, wire.GitHubDeliveryHeader} {
		if d := strings.TrimSpace(h.Get(name)); d != "" {
			ev.ID = d
			break
		}
	}
	if ev.ID == "" {
		ev.ID = ids.New("evt_")
	}
	ev.Data = boundData(ev.Data)
	return wire.ValidEvent(ev)
}

// boundData keeps data within wire.MaxEventDataBytes: an object over the
// bound drops its largest top-level fields until it fits and names them in
// "_omitted"; any other value over it becomes {"_omitted": ["(the whole
// payload)"], "_bytes": n}.
func boundData(data json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if json.Compact(&buf, data) != nil || buf.Len() <= wire.MaxEventDataBytes {
		if buf.Len() == 0 {
			return data
		}
		return buf.Bytes()
	}
	compact := buf.Bytes()
	var top map[string]json.RawMessage
	if json.Unmarshal(compact, &top) != nil || top == nil {
		out, _ := json.Marshal(map[string]any{"_omitted": []string{"(the whole payload)"}, "_bytes": len(compact)})
		return out
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(top[keys[i]]) != len(top[keys[j]]) {
			return len(top[keys[i]]) > len(top[keys[j]])
		}
		return keys[i] < keys[j]
	})
	var omitted []string
	for _, k := range keys {
		out, _ := json.Marshal(withOmitted(top, omitted))
		if len(out) <= wire.MaxEventDataBytes {
			return out
		}
		delete(top, k)
		omitted = append(omitted, k)
	}
	out, _ := json.Marshal(withOmitted(top, omitted))
	return out
}

func withOmitted(top map[string]json.RawMessage, omitted []string) map[string]any {
	out := make(map[string]any, len(top)+1)
	for k, v := range top {
		out[k] = v
	}
	if len(omitted) > 0 {
		out["_omitted"] = omitted
	}
	return out
}
