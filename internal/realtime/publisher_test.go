package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// BE-5/BE-6: the shared publisher asserts its service identity, carries the
// event's tenant, and signs both, so realtime's guarded internal route accepts
// it and persists under the right tenant.
func TestHTTPPublisherSignsServiceIdentityAndTenant(t *testing.T) {
	const key = "internal-key"
	var gotTenant, gotUser, gotPath string
	var gotEv wire.Event
	srv := httptest.NewServer(svcauth.RequireSigned(key, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get(svcauth.HeaderTenant)
		gotUser = r.Header.Get(svcauth.HeaderUser)
		gotPath = r.URL.EscapedPath()
		_ = json.NewDecoder(r.Body).Decode(&gotEv)
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()
	p := NewHTTPPublisher(srv.URL+"/", "activity")
	p.Key = key
	p.Publish(tenant.NewContext(context.Background(), "acme"), wire.Event{RoomID: "room-1", Kind: "agent_messages"})
	if gotTenant != "acme" || gotUser != svcauth.ServiceSubject("activity") {
		t.Fatalf("tenant=%q user=%q, want acme / service:activity", gotTenant, gotUser)
	}
	if gotPath != "/internal/v1/rooms/room-1/publish" || gotEv.Kind != "agent_messages" {
		t.Fatalf("path=%q kind=%q", gotPath, gotEv.Kind)
	}
}

// A tenantless publish is dropped instead of being filed under the default
// tenant (BE-6: replies vanished from non-default tenants' history).
func TestHTTPPublisherDropsTenantlessEvent(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	NewHTTPPublisher(srv.URL, "room").Publish(context.Background(), wire.Event{RoomID: "room-1", Kind: "artifact_updated"})
	if called {
		t.Fatal("tenantless event must not be published")
	}
}
