package svcauth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func appRequest(t *testing.T, k Key, tenantID string, s AppScope) *http.Request {
	t.Helper()
	req, err := k.NewRequest(context.Background(), http.MethodGet, "http://room/internal/v1/app/me", nil, ServiceSubject("gateway"), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.AssertApp(req, s); err != nil {
		t.Fatal(err)
	}
	return req
}

// The app scope reaches a backend only as the gateway signed it: every
// scope value, the tenant and the timestamp are covered.
func TestAppScopeSignedAndVerified(t *testing.T) {
	const key = "k"
	scope := AppScope{RoomID: "room-1", AppID: "art_1", Kind: AppVisitor, Participant: "v-123"}
	got, err := AppScopeOf(key, appRequest(t, key, "org-a", scope))
	if err != nil || got != scope {
		t.Fatalf("AppScopeOf = %+v, %v", got, err)
	}
	if got.ParticipantKey() != "visitor:v-123" {
		t.Fatalf("ParticipantKey = %q", got.ParticipantKey())
	}
	for name, tamper := range map[string]func(r *http.Request){
		"room":        func(r *http.Request) { r.Header.Set(HeaderAppRoom, "room-2") },
		"app":         func(r *http.Request) { r.Header.Set(HeaderApp, "art_2") },
		"participant": func(r *http.Request) { r.Header.Set(HeaderAppParticipant, "member:maya") },
		"tenant":      func(r *http.Request) { r.Header.Set(HeaderTenant, "org-b") },
		"signature":   func(r *http.Request) { r.Header.Set(HeaderAppSig, "00") },
		"unsigned":    func(r *http.Request) { r.Header.Del(HeaderAppSig) },
		"stale": func(r *http.Request) {
			r.Header.Set(HeaderTs, strconv.FormatInt(time.Now().Add(-time.Hour).UnixMilli(), 10))
		},
	} {
		req := appRequest(t, key, "org-a", scope)
		tamper(req)
		if _, err := AppScopeOf(key, req); !errors.Is(err, ErrAppScope) {
			t.Errorf("%s tampered: AppScopeOf = %v, want ErrAppScope", name, err)
		}
	}
	if _, err := AppScopeOf("other-key", appRequest(t, key, "org-a", scope)); !errors.Is(err, ErrAppScope) {
		t.Fatalf("a scope signed with another key = %v", err)
	}
	// Development (no key): read unverified, still validated.
	if got, err := AppScopeOf("", appRequest(t, "", "org-a", scope)); err != nil || got != scope {
		t.Fatalf("dev AppScopeOf = %+v, %v", got, err)
	}
}

func TestAppScopeRefusesMalformed(t *testing.T) {
	ok := AppScope{RoomID: "r", AppID: "a", Kind: AppMember, Participant: "maya"}
	for name, s := range map[string]AppScope{
		"kind":         {RoomID: "r", AppID: "a", Kind: "admin", Participant: "maya"},
		"no room":      {AppID: "a", Kind: AppMember, Participant: "maya"},
		"no app":       {RoomID: "r", Kind: AppMember, Participant: "maya"},
		"no id":        {RoomID: "r", AppID: "a", Kind: AppMember},
		"control char": {RoomID: "r", AppID: "a\nb", Kind: AppMember, Participant: "maya"},
	} {
		req, _ := Key("k").NewRequest(context.Background(), http.MethodGet, "http://x", nil, "service:gateway", "t")
		if err := Key("k").AssertApp(req, s); !errors.Is(err, ErrAppScope) {
			t.Errorf("%s: AssertApp = %v, want ErrAppScope", name, err)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	if err := Key("k").AssertApp(req, ok); !errors.Is(err, ErrAppScope) {
		t.Fatalf("AssertApp before Assert = %v, want ErrAppScope", err)
	}
	if _, err := AppScopeOf("", req); !errors.Is(err, ErrAppScope) {
		t.Fatalf("AppScopeOf with no scope = %v", err)
	}
}
