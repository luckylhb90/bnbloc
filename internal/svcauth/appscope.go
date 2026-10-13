package svcauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The app scope assertion (docs/prd/2026-10-12-interactive-apps.md §4.7):
// the capability an interactive app's request was made with (a member's
// site ticket, a public share link, an agent's job), as the gateway
// resolved it, to (room, app, participant). It rides an internal request
// beside the identity assertion (Assert: the gateway's service subject and
// the room's tenant) under its own signature over the same timestamp and
// tenant, so a holder of neither the key nor a fresh assertion can widen it.
// The room service's app data routes take their whole scope from it and
// nothing from the path, query or body (services/room/appdata.go).
const (
	// HeaderAppRoom carries the room that owns the app.
	HeaderAppRoom = "X-Bnbloc-App-Room"
	// HeaderApp carries the app id (the site artifact's id).
	HeaderApp = "X-Bnbloc-App"
	// HeaderAppParticipant carries "<kind>:<id>" (AppScope.ParticipantKey).
	HeaderAppParticipant = "X-Bnbloc-App-Participant"
	// HeaderAppSig carries the hex HMAC over the app scope (appCompute).
	HeaderAppSig = "X-bnbloc-App-Sig"
)

// AppParticipantKind is who is using an app.
type AppParticipantKind string

const (
	// AppMember is a signed-in member (a member ticket); Participant is
	// their subject.
	AppMember AppParticipantKind = "member"
	// AppAgent is one of the room's agents (its job's capability);
	// Participant is the agent's id.
	AppAgent AppParticipantKind = "agent"
	// AppVisitor is a public-link holder; Participant is the visitor id
	// the share page minted.
	AppVisitor AppParticipantKind = "visitor"
)

// maxAppScopeValue bounds each scope value.
const maxAppScopeValue = 256

// AppScope is one app request's resolved capability.
type AppScope struct {
	RoomID      string
	AppID       string
	Kind        AppParticipantKind
	Participant string
}

// ParticipantKey is the participant as one string, "<kind>:<id>": a
// member and a visitor with the same id are different participants.
func (s AppScope) ParticipantKey() string { return string(s.Kind) + ":" + s.Participant }

// ErrAppScope reports a missing, malformed or forged app scope.
var ErrAppScope = errors.New("invalid app scope assertion")

func (s AppScope) valid() bool {
	switch s.Kind {
	case AppMember, AppAgent, AppVisitor:
	default:
		return false
	}
	for _, v := range []string{s.RoomID, s.AppID, s.Participant} {
		if v == "" || len(v) > maxAppScopeValue || strings.TrimSpace(v) != v || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

// appCompute is the hex HMAC-SHA256 over the scope, bound to the identity
// assertion's timestamp and tenant.
func appCompute(key string, tsMs int64, tenantID string, s AppScope) string {
	mac := hmac.New(sha256.New, []byte(key))
	for _, part := range []string{"app", strconv.FormatInt(tsMs, 10), tenantID, s.RoomID, s.AppID, s.ParticipantKey()} {
		mac.Write([]byte(part))
		mac.Write([]byte("\n"))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// AssertApp adds the app scope to req, which Assert has stamped (the
// tenant and, in a keyed deployment, the timestamp it signs). It is the
// only writer of the app scope headers; an invalid scope is refused.
func (k Key) AssertApp(req *http.Request, s AppScope) error {
	if !s.valid() {
		return ErrAppScope
	}
	sig := ""
	if k != "" {
		ts, err := strconv.ParseInt(req.Header.Get(HeaderTs), 10, 64)
		if err != nil {
			return ErrAppScope // Assert first
		}
		sig = appCompute(string(k), ts, req.Header.Get(HeaderTenant), s)
	}
	req.Header.Set(HeaderAppRoom, s.RoomID)
	req.Header.Set(HeaderApp, s.AppID)
	req.Header.Set(HeaderAppParticipant, s.ParticipantKey())
	req.Header.Del(HeaderAppSig)
	if sig != "" {
		req.Header.Set(HeaderAppSig, sig)
	}
	return nil
}

// AppScopeOf is the verified app scope of a request (the only reader of the
// app scope headers). With a key it requires the scope's signature over the
// request's fresh timestamp and tenant; without one (development) the
// scope is read unverified, like the identity assertion.
func AppScopeOf(key string, r *http.Request) (AppScope, error) {
	s := AppScope{RoomID: r.Header.Get(HeaderAppRoom), AppID: r.Header.Get(HeaderApp)}
	kind, id, _ := strings.Cut(r.Header.Get(HeaderAppParticipant), ":")
	s.Kind, s.Participant = AppParticipantKind(kind), id
	if !s.valid() {
		return AppScope{}, ErrAppScope
	}
	if key == "" {
		return s, nil
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(HeaderTs)), 10, 64)
	if err != nil {
		return AppScope{}, ErrAppScope
	}
	if delta := time.Now().UnixMilli() - ts; delta > freshnessWindowMs || delta < -freshnessWindowMs {
		return AppScope{}, ErrAppScope
	}
	want := appCompute(key, ts, strings.TrimSpace(r.Header.Get(HeaderTenant)), s)
	if !hmac.Equal([]byte(want), []byte(strings.TrimSpace(r.Header.Get(HeaderAppSig)))) {
		return AppScope{}, ErrAppScope
	}
	return s, nil
}
