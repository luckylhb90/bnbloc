package shareagenthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// dctx scopes direct-store seeds to the default tenant, matching what the
// service Handler (svcauth.EnsureTenant) injects for HTTP requests.
func dctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

type onlinePresence struct{}

func (onlinePresence) IsOwnerOnline(string, string) bool { return true }

type fixedOwner struct{ url string }

func (o fixedOwner) OwnerEndpoint(string, string) (string, bool) { return o.url, o.url != "" }

// ownerSecret is the value the owner daemon holds. The test asserts it never
// appears in any bnbloc response — the control-plane must not leak credentials.
const ownerSecret = "sk-OWNER-CREDENTIAL-DO-NOT-LEAK"

func newService(t *testing.T, ownerURL string) (*Service, *store.MemoryGrant) {
	t.Helper()
	grants := store.NewMemoryGrant()
	seq := 0
	controller := &shareagent.Controller{
		Grants:     grants,
		Presence:   onlinePresence{},
		Audit:      grants,
		NewLeaseID: func() string { seq++; return "lease-x" },
		Now:        func() int64 { return 1000 },
	}
	members := membership.NewStatic()
	for _, u := range []string{"alice", "bob", "jack", "reader"} {
		members.Add("room-1", u)
	}
	svc := &Service{
		Controller: controller,
		Grants:     grants,
		Presence:   onlinePresence{},
		Owners:     fixedOwner{url: ownerURL},
		Membership: members,
		Now:        func() int64 { return 1000 },
	}
	return svc, grants
}

func seedGrant(gs *store.MemoryGrant) {
	_ = gs.PutGrant(dctx(), store.Grant{
		GrantID: "g1", RoomID: "room-1", TargetID: "shared-agent:codex",
		OwnerUserID: "alice", GranteeUserID: "bob",
		AllowedModels: []store.AllowedModel{{Model: "gpt-5"}},
		Capabilities:  store.Capabilities{Consult: true}, MaxConcurrent: 2,
	})
}

// An allowed turn is proxied to the owner and the owner credential NEVER
// appears in the bnbloc response body (SEC-1 / R-2 negative control).
func TestTurnProxiesWithoutLeakingCredential(t *testing.T) {
	// Fake owner daemon: holds the secret, runs the turn, returns a result that
	// intentionally does NOT echo the secret. It also asserts bnbloc forwarded no
	// credential to it.
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), ownerSecret) {
			t.Errorf("bnbloc forwarded a credential to the owner: %s", raw)
		}
		_ = ownerSecret // the owner uses its own credential internally
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"turn complete"}`))
	}))
	defer owner.Close()

	svc, gs := newService(t, owner.URL)
	seedGrant(gs)
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	body, _ := json.Marshal(actionBody{GrantID: "g1", Model: "gpt-5", TurnCapability: "consult"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/shared-agents/shared-agent:codex/turn", bytes.NewReader(body))
	req.Header.Set("X-Bnbloc-User", "bob")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), ownerSecret) {
		t.Fatalf("owner credential leaked in bnbloc response: %s", raw)
	}
	if !strings.Contains(string(raw), "turn complete") {
		t.Fatalf("expected proxied result, got: %s", raw)
	}
}

// A denied turn returns 4xx with a stable deny reason and does not proxy.
func TestTurnDeniedReturnsReason(t *testing.T) {
	svc, gs := newService(t, "http://127.0.0.1:0")
	// Grant only allows a different model -> model_out_of_range.
	_ = gs.PutGrant(dctx(), store.Grant{
		GrantID: "g1", RoomID: "room-1", TargetID: "shared-agent:codex",
		OwnerUserID: "alice", GranteeUserID: "bob",
		AllowedModels: []store.AllowedModel{{Model: "other-model"}},
		Capabilities:  store.Capabilities{Consult: true}, MaxConcurrent: 1,
	})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	body, _ := json.Marshal(actionBody{GrantID: "g1", Model: "gpt-5", TurnCapability: "consult"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/shared-agents/shared-agent:codex/turn", bytes.NewReader(body))
	req.Header.Set("X-Bnbloc-User", "bob")
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["denyReason"] != shareagent.DenyModelOutOfRange {
		t.Fatalf("denyReason = %q, want %q", out["denyReason"], shareagent.DenyModelOutOfRange)
	}
}

// The access snapshot carries auditRequired:true and no credential field.
func TestAccessSnapshotHasNoCredential(t *testing.T) {
	svc, gs := newService(t, "")
	seedGrant(gs)
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/shared-agents/shared-agent:codex/access?grantId=g1", nil)
	req.Header.Set("X-Bnbloc-User", "bob")
	resp := mustDo(t, req)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if !strings.Contains(body, `"auditRequired":true`) {
		t.Fatalf("snapshot missing auditRequired: %s", body)
	}
	for _, banned := range []string{"credential", "apiKey", "api_key", "secret", "token"} {
		if strings.Contains(strings.ToLower(body), banned) {
			t.Fatalf("snapshot leaked %q: %s", banned, body)
		}
	}
}

func mustDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	return resp
}
