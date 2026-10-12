package shareagenthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// fakeBridges is a fixed bridge queue: the hosts' live advertisements (the
// GET /v1/my/agents source) and their pending job counts.
type fakeBridges struct {
	ads    map[string][]store.BridgeAgent
	queued map[string][]store.AgentJobCount
}

func (f *fakeBridges) ListBridgeAgentsForOwner(_ context.Context, owner string) []store.BridgeAgent {
	return f.ads[owner]
}

func (f *fakeBridges) PendingAgentJobs(_ context.Context, owner string) ([]store.AgentJobCount, error) {
	return f.queued[owner], nil
}

// platformEnv is shareagent with a published platform agent: root (super
// admin; organization membership is irrelevant to hosting) hosts
// nimbus/codex (default model free-1, online) and nimbus/claude; room-1
// (default tenant) has owner alice, member bob, visitor vic; carol is
// outside the room.
type platformEnv struct {
	svc     *Service
	srv     *httptest.Server
	grants  *store.MemoryGrant
	members *membership.Static
	tenants *store.MemoryTenant
	bridges *fakeBridges
}

func newPlatformEnv(t *testing.T) *platformEnv {
	t.Helper()
	svc, grants := newService(t)
	members := membership.NewStatic()
	members.AddRole("room-1", "alice", store.RoleOwner)
	members.Add("room-1", "bob")
	members.AddRole("room-1", "vic", store.RoleVisitor)
	tenants := store.NewMemoryTenant()
	for _, id := range []string{tenant.Default, "org-b"} {
		_ = tenants.PutTenant(context.Background(), store.Tenant{TenantID: id, Name: id, Status: store.TenantActive})
	}
	for _, u := range []string{"root", "alice", "bob", "vic", "carol"} {
		_ = tenants.PutMember(context.Background(), store.TenantMember{TenantID: tenant.Default, UserID: u, Role: store.OrgRoleMember})
	}
	_ = tenants.PutMember(context.Background(), store.TenantMember{TenantID: "org-b", UserID: "bob", Role: store.OrgRoleMember})
	svc.Membership = members
	svc.AgentHosts = members
	svc.Tenants = tenants
	svc.Settings = store.NewMemoryPlatformSettings()
	bridges := &fakeBridges{
		ads:    map[string][]store.BridgeAgent{"root": {{OwnerUserID: "root", Core: "nimbus", Provider: "codex", TargetID: "nimbus/codex"}}},
		queued: map[string][]store.AgentJobCount{},
	}
	svc.Bridges = bridges
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	e := &platformEnv{svc: svc, srv: srv, grants: grants, members: members, tenants: tenants, bridges: bridges}
	e.publish(t, "root", true, map[string]string{"core": "nimbus", "provider": "codex", "defaultModel": "free-1"}, map[string]string{"core": "nimbus", "provider": "claude"})
	return e
}

// publish saves host's own policy entry.
func (e *platformEnv) publish(t *testing.T, host string, enabled bool, agents ...map[string]string) map[string]any {
	t.Helper()
	if agents == nil {
		agents = []map[string]string{}
	}
	code, out := e.call(t, http.MethodPut, "/v1/platform/platform-agents", host, "", map[string]any{"enabled": enabled, "agents": agents})
	if code != http.StatusOK {
		t.Fatalf("publish as %s = %d %v", host, code, out)
	}
	return out
}

var (
	codexAgent  = map[string]string{"core": "nimbus", "provider": "codex"}
	claudeAgent = map[string]string{"core": "nimbus", "provider": "claude"}
)

func (e *platformEnv) call(t *testing.T, method, path, user, tenantID string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, &buf)
	req.Header.Set(svcauth.HeaderUser, user)
	if tenantID != "" {
		req.Header.Set(svcauth.HeaderTenant, tenantID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func agentsOf(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	raw, _ := json.Marshal(out["agents"])
	var agents []map[string]any
	_ = json.Unmarshal(raw, &agents)
	return agents
}

// hostsOf indexes an agent entry's hosts by host id.
func hostsOf(t *testing.T, agent map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	raw, _ := json.Marshal(agent["hosts"])
	var hosts []map[string]any
	_ = json.Unmarshal(raw, &hosts)
	for _, h := range hosts {
		out[h["hostUserId"].(string)] = h
	}
	return out
}

// byProvider indexes agent entries by provider.
func byProvider(t *testing.T, out map[string]any) map[string]map[string]any {
	t.Helper()
	m := map[string]map[string]any{}
	for _, a := range agentsOf(t, out) {
		m[a["provider"].(string)] = a
	}
	return m
}

func (e *platformEnv) isAgentHost(room, user string) bool {
	m, ok := e.members.Lookup(room, user)
	return ok && m.AgentHost
}

func TestPlatformAgentsPolicyRoutes(t *testing.T) {
	e := newPlatformEnv(t)
	code, out := e.call(t, http.MethodGet, "/v1/platform/platform-agents", "root", "", nil)
	hosts, _ := out["hosts"].([]any)
	if code != http.StatusOK || len(hosts) != 1 {
		t.Fatalf("policy = %d %v", code, out)
	}
	root := hosts[0].(map[string]any)
	if root["hostUserId"] != "root" || root["enabled"] != true || len(root["agents"].([]any)) != 2 ||
		root["agents"].([]any)[0].(map[string]any)["defaultModel"] != "free-1" {
		t.Fatalf("root entry = %v", root)
	}
	for _, bad := range []map[string]string{
		{"core": "nimbus", "provider": " "},
		{"core": "nimbus", "provider": "codex", "defaultModel": strings.Repeat("m", 129)},
	} {
		if code, _ := e.call(t, http.MethodPut, "/v1/platform/platform-agents", "root", "", map[string]any{
			"enabled": true, "agents": []map[string]string{bad},
		}); code != http.StatusBadRequest {
			t.Fatalf("bad agent %v = %d, want 400", bad, code)
		}
	}
	// A second super admin writes only their own entry; duplicates collapse
	// (the first wins); a body hostUserId is not a field.
	code, out = e.call(t, http.MethodPut, "/v1/platform/platform-agents", "root2", "", map[string]any{
		"enabled": false, "hostUserId": "mallory",
		"agents": []map[string]string{{"core": "c", "provider": "p", "defaultModel": " m1 "}, {"core": " c", "provider": "p ", "defaultModel": "m2"}},
	})
	hosts, _ = out["hosts"].([]any)
	if code != http.StatusOK || len(hosts) != 2 {
		t.Fatalf("saved = %d %v", code, out)
	}
	p, _ := store.LoadPlatformAgents(dctx(), e.svc.Settings)
	if len(p.Hosts) != 2 || p.Hosts[1].HostUserID != "root2" || p.Hosts[1].Enabled || len(p.Hosts[1].Agents) != 1 || p.Hosts[1].Agents[0].DefaultModel != "m1" {
		t.Fatalf("root2 entry = %+v", p.Hosts)
	}
	if p.Hosts[0].HostUserID != "root" || !p.Publishes("root", "nimbus", "codex") || !p.Publishes("root", "nimbus", "claude") {
		t.Fatalf("root's entry was touched: %+v", p.Hosts[0])
	}
	// root re-saving keeps root2's entry.
	e.publish(t, "root", true, codexAgent)
	p, _ = store.LoadPlatformAgents(dctx(), e.svc.Settings)
	if len(p.Hosts) != 2 || p.Hosts[1].HostUserID != "root2" || p.Publishes("root", "nimbus", "claude") {
		t.Fatalf("after root re-saves: %+v", p.Hosts)
	}
}

func TestPlatformAgentsMemberList(t *testing.T) {
	e := newPlatformEnv(t)
	code, out := e.call(t, http.MethodGet, "/v1/platform-agents", "bob", "", nil)
	agents := byProvider(t, out)
	if code != http.StatusOK || len(agents) != 2 {
		t.Fatalf("list = %d %v", code, out)
	}
	codex := hostsOf(t, agents["codex"])["root"]
	if agents["codex"]["online"] != true || agents["codex"]["core"] != "nimbus" || codex["online"] != true || codex["targetId"] != "nimbus/codex" ||
		codex["defaultModel"] != "free-1" || codex["queued"] != float64(0) {
		t.Fatalf("codex = %v; want online with the advertised target and the default model", agents["codex"])
	}
	claude := hostsOf(t, agents["claude"])["root"]
	if agents["claude"]["online"] != false || claude["online"] != false || claude["targetId"] != "" || claude["defaultModel"] != nil {
		t.Fatalf("claude = %v; want offline (not advertised), no default model", agents["claude"])
	}
	// Platform agents are platform-wide: the host (a super admin, not an
	// org-b member) publishes them in org-b too.
	if _, out := e.call(t, http.MethodGet, "/v1/platform-agents", "bob", "org-b", nil); len(agentsOf(t, out)) != 2 {
		t.Fatalf("org-b list = %v, want both published agents", out)
	}
	// Off: empty.
	e.publish(t, "root", false, codexAgent)
	if _, out := e.call(t, http.MethodGet, "/v1/platform-agents", "bob", "", nil); len(agentsOf(t, out)) != 0 {
		t.Fatalf("disabled list = %v, want empty", out)
	}
}

// Two hosts publishing the same agent are one entry with each host's state:
// online from its advertisement, queued from its pending jobs.
func TestPlatformAgentsMemberListMultiHost(t *testing.T) {
	e := newPlatformEnv(t)
	e.publish(t, "root2", true, map[string]string{"core": "nimbus", "provider": "codex", "defaultModel": "free-2"})
	e.bridges.ads["root2"] = []store.BridgeAgent{{OwnerUserID: "root2", Core: "nimbus", Provider: "codex", TargetID: "nimbus/codex"}}
	e.bridges.queued["root"] = []store.AgentJobCount{{Core: "nimbus", Provider: "codex", Count: 3}, {Core: "nimbus", Provider: "claude", Count: 1}}
	e.bridges.queued["root2"] = []store.AgentJobCount{{Core: "nimbus", Provider: "codex", Count: 1}}
	_, out := e.call(t, http.MethodGet, "/v1/platform-agents", "bob", "", nil)
	agents := agentsOf(t, out)
	if len(agents) != 2 || agents[0]["provider"] != "codex" || agents[1]["provider"] != "claude" {
		t.Fatalf("agents = %v; want codex then claude (policy order)", agents)
	}
	hosts := hostsOf(t, agents[0])
	if len(hosts) != 2 || hosts["root"]["queued"] != float64(3) || hosts["root2"]["queued"] != float64(1) ||
		hosts["root2"]["online"] != true || hosts["root2"]["defaultModel"] != "free-2" || hosts["root"]["defaultModel"] != "free-1" {
		t.Fatalf("codex hosts = %v", hosts)
	}
	if h := hostsOf(t, agents[1]); len(h) != 1 || h["root"]["queued"] != float64(1) || h["root"]["online"] != false {
		t.Fatalf("claude hosts = %v", h)
	}
	// One host offline: the agent stays online through the other.
	e.bridges.ads["root"] = nil
	_, out = e.call(t, http.MethodGet, "/v1/platform-agents", "bob", "", nil)
	codex := byProvider(t, out)["codex"]
	if codex["online"] != true || hostsOf(t, codex)["root"]["online"] != false {
		t.Fatalf("codex with root offline = %v", codex)
	}
}

func TestPlatformAgentAddAuthorization(t *testing.T) {
	e := newPlatformEnv(t)
	add := map[string]string{"core": "nimbus", "provider": "codex"}
	for _, c := range []struct {
		user string
		want int
	}{
		{"vic", http.StatusForbidden},   // a visitor may not chat
		{"carol", http.StatusForbidden}, // not a room member
	} {
		if code, out := e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", c.user, "", add); code != c.want {
			t.Fatalf("add as %s = %d %v, want %d", c.user, code, out, c.want)
		}
	}
	if code, _ := e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", map[string]string{"core": "nimbus", "provider": "gemini"}); code != http.StatusNotFound {
		t.Fatalf("unpublished agent = %d, want 404", code)
	}
	// An agent host cannot add (it is not a person of the room).
	_ = e.members.PutAgentHost(context.Background(), "", "room-1", "dave")
	if code, _ := e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "dave", "", add); code != http.StatusForbidden {
		t.Fatalf("add by an agent host = %d, want 403", code)
	}
	// A room in a tenant the host is not a member of: platform-wide, allowed.
	e.members.Add("room-b", "bob")
	code, out := e.call(t, http.MethodPost, "/v1/rooms/room-b/platform-agents", "bob", "org-b", add)
	if code != http.StatusCreated {
		t.Fatalf("add in another organization = %d %v, want 201", code, out)
	}
	if !e.isAgentHost("room-b", "root") {
		t.Fatal("the add left no agent-host membership")
	}
	if len(e.grants.RoomGrants(tctx("org-b"), "room-b")) != 1 {
		t.Fatal("the add left no platform grant in the other organization")
	}
}

func TestPlatformAgentAddListRemove(t *testing.T) {
	e := newPlatformEnv(t)
	codex := map[string]string{"core": "nimbus", "provider": "codex"}
	code, out := e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", codex)
	if code != http.StatusCreated {
		t.Fatalf("add = %d %v", code, out)
	}
	agent := out["agent"].(map[string]any)
	if h := hostsOf(t, agent); len(h) != 1 || h["root"]["online"] != true || h["root"]["targetId"] != "nimbus/codex" || agent["addedBy"] != "bob" || agent["online"] != true {
		t.Fatalf("added agent = %v", agent)
	}
	if !e.isAgentHost("room-1", "root") {
		t.Fatal("adding must give the host an agent-host membership")
	}
	g, err := e.grants.GetGrant(dctx(), platformGrantID(dctx(), "room-1", "root", "nimbus:codex"))
	if err != nil || g.Source != store.GrantSourcePlatform || g.RunQuota != nil || g.CostAllowance != nil || g.ExpiresAtMs != nil ||
		len(g.AllowedModels) != 0 || g.TargetID != "shared-agent:nimbus:codex" || g.OwnerUserID != "root" || g.CreatedBy != "bob" {
		t.Fatalf("platform grant = %+v %v", g, err)
	}
	// Idempotent: a second add (by anyone) answers the same agent.
	code, out = e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "alice", "", codex)
	if code != http.StatusOK || out["agent"].(map[string]any)["addedBy"] != "bob" {
		t.Fatalf("re-add = %d %v; want 200 with the original adder", code, out)
	}
	if _, out := e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", map[string]string{"core": "nimbus", "provider": "claude"}); out["agent"] == nil {
		t.Fatalf("add claude = %v", out)
	}
	code, out = e.call(t, http.MethodGet, "/v1/rooms/room-1/platform-agents", "alice", "", nil)
	if agents := agentsOf(t, out); code != http.StatusOK || len(agents) != 2 || agents[0]["addedBy"] != "bob" || agents[0]["addedAtMs"] == nil {
		t.Fatalf("room list = %d %v", code, out)
	}
	// Platform agents are not owner authorizations in the borrow listing.
	req := httptest.NewRequest(http.MethodGet, "/v1/rooms/room-1/shared-agents", nil).WithContext(dctx())
	req.SetPathValue("roomId", "room-1")
	req.Header.Set("X-Bnbloc-User", "bob")
	w := httptest.NewRecorder()
	e.svc.handleListSharedAgents(w, req)
	if w.Body.String() != "[]\n" {
		t.Fatalf("shared-agents listing = %s; platform grants have their own listing", w.Body.String())
	}

	// A visitor (not the host) may not remove.
	if code, _ := e.call(t, http.MethodDelete, "/v1/rooms/room-1/platform-agents/codex?core=nimbus", "vic", "", nil); code != http.StatusForbidden {
		t.Fatalf("remove by a visitor = %d, want 403", code)
	}
	// Removing one keeps the membership while another of the host's agents
	// remains; removing the last drops it.
	if code, _ := e.call(t, http.MethodDelete, "/v1/rooms/room-1/platform-agents/codex?core=nimbus", "bob", "", nil); code != http.StatusNoContent {
		t.Fatalf("remove codex = %d", code)
	}
	if !e.isAgentHost("room-1", "root") {
		t.Fatal("the host lost its membership while claude is still in the room")
	}
	if live, _, _ := e.svc.roomAuthorized(dctx(), "room-1", "root", "nimbus:codex"); live {
		t.Fatal("a removed platform agent is still authorized")
	}
	// The host itself may remove (it is only an agent host here).
	if code, _ := e.call(t, http.MethodDelete, "/v1/rooms/room-1/platform-agents/claude?core=nimbus", "root", "", nil); code != http.StatusNoContent {
		t.Fatalf("remove claude by the host = %d", code)
	}
	if e.isAgentHost("room-1", "root") {
		t.Fatal("removing the host's last platform agent must drop its membership")
	}
	if code, _ := e.call(t, http.MethodDelete, "/v1/rooms/room-1/platform-agents/claude?core=nimbus", "bob", "", nil); code != http.StatusNoContent {
		t.Fatalf("removing an absent agent = %d, want 204", code)
	}
	// A re-add after removal is a fresh grant with its new adder.
	code, out = e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "alice", "", codex)
	if code != http.StatusCreated || out["agent"].(map[string]any)["addedBy"] != "alice" || !e.isAgentHost("room-1", "root") {
		t.Fatalf("re-add = %d %v", code, out)
	}
}

// A human member who hosts the platform agent keeps their membership: no
// agent-host row is made, and removal never removes them.
func TestPlatformAgentHostAlreadyHumanMember(t *testing.T) {
	e := newPlatformEnv(t)
	e.members.Add("room-1", "root")
	if code, _ := e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", map[string]string{"core": "nimbus", "provider": "codex"}); code != http.StatusCreated {
		t.Fatalf("add = %d", code)
	}
	if e.call(t, http.MethodDelete, "/v1/rooms/room-1/platform-agents/codex?core=nimbus", "bob", "", nil); !membership.HumanMember(e.members, "room-1", "root") {
		t.Fatal("removing the platform agent removed its host's human membership")
	}
}

// Unpublishing takes effect at read time: the room list drops the agent and
// its host's membership; publishing again restores both.
func TestPlatformAgentUnpublishReconciles(t *testing.T) {
	e := newPlatformEnv(t)
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", map[string]string{"core": "nimbus", "provider": "codex"})
	if live, _, _ := e.svc.roomAuthorized(dctx(), "room-1", "root", "nimbus:codex"); !live {
		t.Fatal("a published platform agent must be authorized")
	}
	e.call(t, http.MethodPut, "/v1/platform/platform-agents", "root", "", map[string]any{"enabled": true, "agents": []map[string]string{{"core": "nimbus", "provider": "claude"}}})
	if live, _, _ := e.svc.roomAuthorized(dctx(), "room-1", "root", "nimbus:codex"); live {
		t.Fatal("an unpublished platform agent must read as not authorized")
	}
	if _, out := e.call(t, http.MethodGet, "/v1/rooms/room-1/platform-agents", "bob", "", nil); len(agentsOf(t, out)) != 0 {
		t.Fatalf("room list after unpublish = %v", out)
	}
	if e.isAgentHost("room-1", "root") {
		t.Fatal("the stale agent-host membership survived the listing")
	}
	e.call(t, http.MethodPut, "/v1/platform/platform-agents", "root", "", map[string]any{"enabled": true, "agents": []map[string]string{{"core": "nimbus", "provider": "codex"}}})
	if _, out := e.call(t, http.MethodGet, "/v1/rooms/room-1/platform-agents", "bob", "", nil); len(agentsOf(t, out)) != 1 || !e.isAgentHost("room-1", "root") {
		t.Fatalf("republished: list %v, agent host %v", out, e.isAgentHost("room-1", "root"))
	}
}

// Reserve: a platform grant reserves unlimited runs for a sandboxed drive
// and answers sandboxRequired; an unsandboxed drive is refused before
// anything is reserved; an unpublished agent is platform_agent_unavailable;
// the org ceiling does not apply, and the runs are still audited.
func TestPlatformAgentReserve(t *testing.T) {
	e := newPlatformEnv(t)
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", map[string]string{"core": "nimbus", "provider": "codex"})
	// An org ceiling already reached by owner-issued borrows.
	_ = e.tenants.PutTenant(context.Background(), store.Tenant{TenantID: tenant.Default, Name: "d", Status: store.TenantActive, RunQuota: 1})
	reserve := func(body reserveBorrowBody) (int, map[string]any) {
		return borrowCall(t, e.svc, tenant.Default, "root", "root", "nimbus:codex", "reserve", body)
	}
	for i := range 3 {
		code, out := reserve(reserveBorrowBody{Borrower: "bob", Sandboxed: true})
		if code != http.StatusOK || out["reserved"] != true || out["sandboxRequired"] != true {
			t.Fatalf("sandboxed reserve %d = %d %v", i, code, out)
		}
	}
	code, out := reserve(reserveBorrowBody{Borrower: "bob"})
	if code != http.StatusForbidden || out["denyReason"] != shareagent.DenySandboxRequired || out["sandboxRequired"] != true {
		t.Fatalf("unsandboxed reserve = %d %v", code, out)
	}
	g, _ := e.grants.GetGrant(dctx(), platformGrantID(dctx(), "room-1", "root", "nimbus:codex"))
	if g.RunsUsed != 3 {
		t.Fatalf("runs used = %d, want 3 (a refused drive reserves nothing)", g.RunsUsed)
	}
	allowed, denied := 0, 0
	for _, rec := range e.grants.AuditRecords(dctx(), "room-1", 0, 0) {
		if rec.GrantID != g.GrantID || rec.ActorUserID != "bob" || rec.OwnerUserID != "root" {
			continue
		}
		if rec.Decision == "allow" {
			allowed++
		} else if rec.DenyReason == shareagent.DenySandboxRequired {
			denied++
		}
	}
	if allowed != 3 || denied != 1 {
		t.Fatalf("audit allow=%d sandbox denies=%d; want 3 and 1", allowed, denied)
	}
	// Unpublished at read time.
	e.call(t, http.MethodPut, "/v1/platform/platform-agents", "root", "", map[string]any{"enabled": false, "agents": []map[string]string{{"core": "nimbus", "provider": "codex"}}})
	code, out = reserve(reserveBorrowBody{Borrower: "bob", Sandboxed: true})
	if code != http.StatusForbidden || out["denyReason"] != shareagent.DenyPlatformAgentUnavailable {
		t.Fatalf("reserve after unpublish = %d %v", code, out)
	}
	// Republished: usable again. Organization membership of the host plays
	// no part (platform agents are platform-wide).
	e.call(t, http.MethodPut, "/v1/platform/platform-agents", "root", "", map[string]any{"enabled": true, "agents": []map[string]string{{"core": "nimbus", "provider": "codex"}}})
	_ = e.tenants.RemoveMember(context.Background(), tenant.Default, "root")
	if code, _ := reserve(reserveBorrowBody{Borrower: "bob", Sandboxed: true}); code != http.StatusOK {
		t.Fatalf("republished reserve = %d", code)
	}
}

// An owner-issued grant still answers sandboxRequired false (the tenant
// policy, not the authority, decides its sandbox).
func TestOwnerGrantReserveNotSandboxRequired(t *testing.T) {
	svc, gs := newService(t)
	future := int64(1_000_000_000_000)
	_ = gs.PutGrant(dctx(), store.Grant{
		GrantID: roomAuthGrantID(dctx(), "room-1", "alice", "codex"), RoomID: "room-1",
		TargetID: "shared-agent:codex", OwnerUserID: "alice", GranteeUserID: "*", ExpiresAtMs: &future,
	})
	code, out := reserveRun(t, svc, tenant.Default, "alice", "codex", "bob", "")
	if code != http.StatusOK || out["sandboxRequired"] != false {
		t.Fatalf("owner-issued reserve = %d %v", code, out)
	}
}

// A platform agent removed from the room is not authorized (not a sandbox
// refusal).
func TestPlatformAgentReserveAfterRemoval(t *testing.T) {
	e := newPlatformEnv(t)
	e.members.Add("room-1", "root") // a human member: its membership outlives the removal
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", map[string]string{"core": "nimbus", "provider": "codex"})
	e.call(t, http.MethodDelete, "/v1/rooms/room-1/platform-agents/codex?core=nimbus", "bob", "", nil)
	code, out := borrowCall(t, e.svc, tenant.Default, "root", "root", "nimbus:codex", "reserve", reserveBorrowBody{Borrower: "bob"})
	if code != http.StatusForbidden || out["denyReason"] != shareagent.DenyOwnerNotAuthorized {
		t.Fatalf("reserve after removal = %d %v", code, out)
	}
}

// Adding a platform agent published by two hosts gives the room a grant and
// an agent-host membership for each; a host publishing it later is added
// when the room's agents are next listed; removal revokes every host's.
func TestPlatformAgentMultiHostAddListReconcile(t *testing.T) {
	e := newPlatformEnv(t)
	e.publish(t, "root2", true, codexAgent)
	code, out := e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", codexAgent)
	if code != http.StatusCreated {
		t.Fatalf("add = %d %v", code, out)
	}
	if h := hostsOf(t, out["agent"].(map[string]any)); len(h) != 2 || h["root"] == nil || h["root2"] == nil {
		t.Fatalf("added agent hosts = %v", h)
	}
	for _, host := range []string{"root", "root2"} {
		if !e.isAgentHost("room-1", host) {
			t.Fatalf("%s has no agent-host membership", host)
		}
		if live, _, _ := e.svc.roomAuthorized(dctx(), "room-1", host, "nimbus:codex"); !live {
			t.Fatalf("%s's platform grant is not live", host)
		}
	}
	// Idempotent once every host serves the room.
	if code, _ := e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "alice", "", codexAgent); code != http.StatusOK {
		t.Fatalf("re-add = %d, want 200", code)
	}
	// claude is added (root only); root2 publishes it afterwards: the next
	// listing extends it to root2, keeping the original adder.
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", claudeAgent)
	e.publish(t, "root2", true, codexAgent, claudeAgent)
	_, out = e.call(t, http.MethodGet, "/v1/rooms/room-1/platform-agents", "alice", "", nil)
	agents := byProvider(t, out)
	if len(agents) != 2 || len(hostsOf(t, agents["claude"])) != 2 || agents["claude"]["addedBy"] != "bob" || len(hostsOf(t, agents["codex"])) != 2 {
		t.Fatalf("room list after root2 published claude = %v", out)
	}
	g, err := e.grants.GetGrant(dctx(), platformGrantID(dctx(), "room-1", "root2", "nimbus:claude"))
	if err != nil || g.Source != store.GrantSourcePlatform || g.CreatedBy != "bob" {
		t.Fatalf("extended grant = %+v %v", g, err)
	}
	// root unpublishes codex: the room serves codex through root2 only; root
	// keeps its membership for claude.
	e.publish(t, "root", true, claudeAgent)
	_, out = e.call(t, http.MethodGet, "/v1/rooms/room-1/platform-agents", "alice", "", nil)
	if h := hostsOf(t, byProvider(t, out)["codex"]); len(h) != 1 || h["root2"] == nil || !e.isAgentHost("room-1", "root") {
		t.Fatalf("codex hosts after root unpublished = %v (root agent host %v)", h, e.isAgentHost("room-1", "root"))
	}
	// Removing codex revokes it for every host (root2 keeps claude).
	if code, _ := e.call(t, http.MethodDelete, "/v1/rooms/room-1/platform-agents/codex?core=nimbus", "bob", "", nil); code != http.StatusNoContent {
		t.Fatalf("remove codex = %d", code)
	}
	for _, host := range []string{"root", "root2"} {
		if live, _, _ := e.svc.roomAuthorized(dctx(), "room-1", host, "nimbus:codex"); live {
			t.Fatalf("%s's codex grant survived the removal", host)
		}
	}
	if !e.isAgentHost("room-1", "root2") {
		t.Fatal("root2 lost its membership while claude is still in the room")
	}
	// A removed agent is not extended again by a listing.
	e.call(t, http.MethodGet, "/v1/rooms/room-1/platform-agents", "alice", "", nil)
	if live, _, _ := e.svc.roomAuthorized(dctx(), "room-1", "root2", "nimbus:codex"); live {
		t.Fatal("the listing re-added a removed agent")
	}
	// Any host serving it may remove it; the last agent drops both hosts.
	if code, _ := e.call(t, http.MethodDelete, "/v1/rooms/room-1/platform-agents/claude?core=nimbus", "root2", "", nil); code != http.StatusNoContent {
		t.Fatalf("remove claude by root2 = %d", code)
	}
	if e.isAgentHost("room-1", "root") || e.isAgentHost("room-1", "root2") {
		t.Fatal("removing the last platform agent must drop every host's membership")
	}
}

// The room listing reports each host's online and queued state.
func TestRoomPlatformAgentsHostState(t *testing.T) {
	e := newPlatformEnv(t)
	e.publish(t, "root2", true, codexAgent)
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", codexAgent)
	e.bridges.queued["root"] = []store.AgentJobCount{{Core: "nimbus", Provider: "codex", Count: 4}}
	_, out := e.call(t, http.MethodGet, "/v1/rooms/room-1/platform-agents", "bob", "", nil)
	h := hostsOf(t, byProvider(t, out)["codex"])
	if h["root"]["online"] != true || h["root"]["queued"] != float64(4) || h["root"]["defaultModel"] != "free-1" ||
		h["root2"]["online"] != false || h["root2"]["queued"] != float64(0) || h["root2"]["targetId"] != "" {
		t.Fatalf("room hosts = %v", h)
	}
}

// The reserve answer of a platform grant carries the host's default model,
// and a run that named no model is audited with it.
func TestPlatformAgentReserveDefaultModel(t *testing.T) {
	e := newPlatformEnv(t)
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", codexAgent)
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", claudeAgent)
	code, out := borrowCall(t, e.svc, tenant.Default, "root", "root", "nimbus:codex", "reserve", reserveBorrowBody{Borrower: "bob", Sandboxed: true})
	if code != http.StatusOK || out["defaultModel"] != "free-1" {
		t.Fatalf("reserve = %d %v; want defaultModel free-1", code, out)
	}
	code, out = borrowCall(t, e.svc, tenant.Default, "root", "root", "nimbus:codex", "reserve", reserveBorrowBody{Borrower: "bob", Model: "picked", Sandboxed: true})
	if code != http.StatusOK || out["defaultModel"] != "free-1" {
		t.Fatalf("reserve with a model = %d %v", code, out)
	}
	if code, out := borrowCall(t, e.svc, tenant.Default, "root", "root", "nimbus:claude", "reserve", reserveBorrowBody{Borrower: "bob", Sandboxed: true}); code != http.StatusOK || out["defaultModel"] != nil {
		t.Fatalf("reserve without a default = %d %v", code, out)
	}
	var models []string
	for _, rec := range e.grants.AuditRecords(dctx(), "room-1", 0, 0) {
		if rec.Decision == "allow" && rec.GrantID == platformGrantID(dctx(), "room-1", "root", "nimbus:codex") {
			models = append(models, rec.Model)
		}
	}
	if len(models) != 2 || !((models[0] == "free-1" && models[1] == "picked") || (models[0] == "picked" && models[1] == "free-1")) {
		t.Fatalf("audited models = %v; want the default for the run that named none", models)
	}
}

// The usage dashboard aggregates platform-grant runs only, across every
// organization: today/month totals, top members and rooms, recent runs.
func TestPlatformAgentsUsage(t *testing.T) {
	e := newPlatformEnv(t)
	// 2026-10-10 12:00 UTC.
	now := int64(1791633600000)
	e.svc.Now = func() int64 { return now }
	e.svc.RoomName = func(ctx context.Context, roomID string) string {
		return tenant.FromContext(ctx) + ":" + roomID + "-name"
	}
	e.members.Add("room-b", "bob")
	e.members.Add("room-b", "carol")
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", codexAgent)
	e.call(t, http.MethodPost, "/v1/rooms/room-b/platform-agents", "bob", "org-b", codexAgent)
	// An owner-issued borrow in room-1 is not a platform run.
	future := now * 2
	_ = e.grants.PutGrant(dctx(), store.Grant{GrantID: roomAuthGrantID(dctx(), "room-1", "alice", "codex"), RoomID: "room-1",
		TargetID: "shared-agent:codex", OwnerUserID: "alice", GranteeUserID: "*", ExpiresAtMs: &future})
	run := func(tid, room, borrower, provider, owner string, atMs int64, cost float64, tokens int64) {
		t.Helper()
		ctx := tctx(tid)
		c, in := cost, tokens
		grant := roomAuthGrantID(ctx, room, owner, provider)
		if owner == "root" { // the platform host
			grant = platformGrantID(ctx, room, owner, provider)
		}
		if err := e.grants.AppendAudit(ctx, store.AuditRecord{
			RoomID: room, GrantID: grant, ActorUserID: borrower, OwnerUserID: owner,
			Action: "turn", Decision: "allow", Model: "m-" + borrower, AtMs: atMs, Cost: &c, InputTokens: &in,
		}); err != nil {
			t.Fatal(err)
		}
	}
	lastMonth := int64(1790000000000) // 2026-09-21
	yesterday := now - 24*3600*1000
	run(tenant.Default, "room-1", "bob", "nimbus:codex", "root", lastMonth, 9, 900)
	run(tenant.Default, "room-1", "bob", "nimbus:codex", "root", yesterday, 0.5, 100)
	run(tenant.Default, "room-1", "alice", "nimbus:codex", "root", now-1000, 0.25, 50)
	run("org-b", "room-b", "carol", "nimbus:codex", "root", now-500, 0, 10)
	run("org-b", "room-b", "carol", "nimbus:codex", "root", now-400, 0, 10)
	run("org-b", "room-b", "carol", "nimbus:codex", "root", now-300, 0, 10)
	run(tenant.Default, "room-1", "bob", "codex", "alice", now-200, 5, 500) // owner-issued
	code, out := e.call(t, http.MethodGet, "/v1/platform/platform-agents/usage", "root", "", nil)
	if code != http.StatusOK {
		t.Fatalf("usage = %d %v", code, out)
	}
	raw, _ := json.Marshal(out)
	var u wire.PlatformAgentUsageReply
	_ = json.Unmarshal(raw, &u)
	if u.Month != (wire.PlatformAgentUsageTotals{Runs: 5, Members: 3, Rooms: 2, Tokens: 180, Cost: 0.75}) {
		t.Fatalf("month = %+v", u.Month)
	}
	if u.Today != (wire.PlatformAgentUsageTotals{Runs: 4, Members: 2, Rooms: 2, Tokens: 80, Cost: 0.25}) {
		t.Fatalf("today = %+v", u.Today)
	}
	if len(u.TopMembers) != 3 || u.TopMembers[0].MemberID != "carol" || u.TopMembers[0].Runs != 3 || u.TopMembers[1].MemberID != "bob" {
		t.Fatalf("top members = %+v", u.TopMembers)
	}
	if len(u.TopRooms) != 2 || u.TopRooms[0].RoomID != "room-b" || u.TopRooms[0].TenantID != "org-b" || u.TopRooms[0].RoomName != "org-b:room-b-name" || u.TopRooms[0].Runs != 3 {
		t.Fatalf("top rooms = %+v", u.TopRooms)
	}
	if len(u.Recent) != 5 || u.Recent[0].AtMs != now-300 || u.Recent[0].Agent != "nimbus/codex" || u.Recent[0].HostUserID != "root" ||
		u.Recent[0].Model != "m-carol" || u.Recent[0].RoomName != "org-b:room-b-name" || u.Recent[4].MemberID != "bob" || u.Recent[4].Cost != 0.5 {
		t.Fatalf("recent = %+v", u.Recent)
	}
	if code, _ := e.call(t, http.MethodGet, "/v1/platform/platform-agents/usage?tzOffsetMin=9999", "root", "", nil); code != http.StatusBadRequest {
		t.Fatalf("bad tzOffsetMin = %d, want 400", code)
	}
}

// The owner's accountability view lists only usable grants: a platform
// grant whose agent is no longer published is gone from it at read time,
// like every other reader of liveAuthorization / usableGrant.
func TestAccountabilityDropsUnpublishedPlatformGrant(t *testing.T) {
	e := newPlatformEnv(t)
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", map[string]string{"core": "nimbus", "provider": "codex"})
	usage := func() []any {
		t.Helper()
		code, out := e.call(t, http.MethodGet, "/v1/rooms/room-1/shared-agents-usage", "root", "", nil)
		if code != http.StatusOK {
			t.Fatalf("usage = %d %v", code, out)
		}
		list, _ := out["authorizations"].([]any)
		return list
	}
	if n := len(usage()); n != 1 {
		t.Fatalf("published platform grant: %d authorizations, want 1", n)
	}
	e.call(t, http.MethodPut, "/v1/platform/platform-agents", "root", "", map[string]any{"enabled": true, "agents": []map[string]string{{"core": "nimbus", "provider": "claude"}}})
	if n := len(usage()); n != 0 {
		t.Fatalf("unpublished platform grant: %d authorizations, want 0", n)
	}
}
