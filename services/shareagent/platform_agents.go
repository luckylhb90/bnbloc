package shareagenthttp

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Platform agents (docs/prd/2026-10-10-zero-install-onboarding.md §2,
// docs/prd/2026-10-10-platform-agents-v2-onboarding.md §2–4). Super admins
// publish some of their own agents (the policy: platform setting
// platform_agents, one entry per host). A platform agent is a (core,
// provider) published by one or more hosts. A member who may chat adds one
// to a room: for EVERY host publishing it, that puts a room authorization
// grant for the host's agent with Source platform (no run quota, no cost
// allowance, no model range, no expiry) under the same deterministic id as
// an owner-issued one, so the host's bridge drives it through the ordinary
// borrow path (reserve/record), and gives the host a hidden agent-host
// membership of the room (the room service's internal route) so their bridge
// serves the room. Unpublishing takes effect at read time: liveAuthorization
// (reserve, the borrower's check-on-send) refuses a platform grant whose
// host no longer publishes the agent, and the room listing reconciles the
// room with the policy both ways (extends an added agent to hosts that
// published it since; converges each host's membership on its available
// grants).

// BridgeAgents is what the platform-agent listings read from the bridge
// queue (store.BridgeJobStore): the hosts' live advertisements (online,
// targetId) and their not-yet-started jobs (queued).
type BridgeAgents interface {
	shareagent.AgentAdvertisements
	PendingAgentJobs(ctx context.Context, ownerUserID string) ([]store.AgentJobCount, error)
}

// AgentHosts adds and removes a host's agent-host room membership (the room
// service's internal route; membership.RoomClient). tenantID is the room's.
type AgentHosts interface {
	PutAgentHost(ctx context.Context, tenantID, roomID, userID string) error
	DeleteAgentHost(ctx context.Context, tenantID, roomID, userID string) error
}

// errNotPublished is the 404 for an agent no host publishes.
const errNotPublished = "platform agent is not published"

// platformAgentsReady answers 501 when the policy store or the tenant
// registry is not configured.
func (s *Service) platformAgentsReady(w http.ResponseWriter) bool {
	if s.Settings == nil || s.Tenants == nil {
		httpx.Unavailable(w, "platform agents")
		return false
	}
	return true
}

// platformPolicy reads the policy, answering 500 on a read failure.
func (s *Service) platformPolicy(w http.ResponseWriter, r *http.Request) (wire.PlatformAgentsPolicy, bool) {
	p, err := store.LoadPlatformAgents(r.Context(), s.Settings)
	if err != nil {
		obs.ServerError(w, "read platform agents policy failed", err)
		httpx.Error(w, "read platform agents policy failed", http.StatusInternalServerError)
		return wire.PlatformAgentsPolicy{}, false
	}
	return p, true
}

// handleGetPlatformAgentsPolicy is GET /v1/platform/platform-agents
// (super-admin; manage_platform at the gateway): every host's entry.
func (s *Service) handleGetPlatformAgentsPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.platformAgentsReady(w) {
		return
	}
	p, ok := s.platformPolicy(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

// handlePutPlatformAgentsPolicy is PUT /v1/platform/platform-agents
// {enabled, agents:[{core, provider, defaultModel?}]}: it replaces the
// caller's own host entry and keeps every other host's. Every agent must be
// a non-empty core/provider pair; duplicates collapse (the first wins).
// Answers the whole policy.
func (s *Service) handlePutPlatformAgentsPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.platformAgentsReady(w) {
		return
	}
	host := callerSubject(r)
	if host == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	var body struct {
		Enabled bool                     `json:"enabled"`
		Agents  []wire.PlatformAgentSpec `json:"agents"`
	}
	if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &body) {
		return
	}
	entry := wire.PlatformAgentHost{HostUserID: host, Enabled: body.Enabled, Agents: []wire.PlatformAgentSpec{}}
	seen := map[wire.PlatformAgentRef]bool{}
	for _, a := range body.Agents {
		a = wire.PlatformAgentSpec{Core: strings.TrimSpace(a.Core), Provider: strings.TrimSpace(a.Provider), DefaultModel: strings.TrimSpace(a.DefaultModel)}
		if a.Core == "" || a.Provider == "" {
			httpx.Error(w, "each agent needs a core and a provider", http.StatusBadRequest)
			return
		}
		if len([]rune(a.DefaultModel)) > maxRecordedModel {
			httpx.Error(w, "defaultModel is too long", http.StatusBadRequest)
			return
		}
		if !seen[a.Ref()] {
			seen[a.Ref()] = true
			entry.Agents = append(entry.Agents, a)
		}
	}
	p, err := store.UpdatePlatformAgents(r.Context(), s.Settings, host, s.Now(), func(cur wire.PlatformAgentsPolicy) wire.PlatformAgentsPolicy {
		return cur.WithHost(entry)
	})
	if err != nil {
		obs.ServerError(w, "save platform agents policy failed", err)
		httpx.Error(w, "save platform agents policy failed", http.StatusInternalServerError)
		return
	}
	names := make([]string, 0, len(entry.Agents))
	for _, a := range entry.Agents {
		name := a.Core + "/" + a.Provider
		if a.DefaultModel != "" {
			name += "@" + a.DefaultModel
		}
		names = append(names, name)
	}
	s.audit(r.Context(), store.ExtAuditRecord{
		Kind: wire.AuditKindPlatformAgent, Name: host, Action: "policy", Actor: host,
		Detail: "enabled=" + boolWord(entry.Enabled) + " agents=" + strings.Join(names, ","),
	})
	httpx.WriteJSON(w, http.StatusOK, p)
}

// platformGrantSpec returns the published spec of a platform grant's agent
// while its host still publishes it. Platform agents are available in every
// organization: a host is a super admin acting platform-wide. An unreadable
// policy fails closed.
func (s *Service) platformGrantSpec(ctx context.Context, g store.Grant) (wire.PlatformAgentSpec, bool) {
	if s.Settings == nil {
		return wire.PlatformAgentSpec{}, false
	}
	ref, ok := shareagent.PlatformAgentOfTarget(g.TargetID)
	if !ok {
		return wire.PlatformAgentSpec{}, false
	}
	p, err := store.LoadPlatformAgents(ctx, s.Settings)
	if err != nil {
		slog.Warn("bnbloc shareagent: platform agents policy unreadable; refusing the platform grant",
			"event", "bnbloc.shareagent.platform_policy_unreadable", "error", err)
		return wire.PlatformAgentSpec{}, false
	}
	return p.Agent(g.OwnerUserID, ref.Core, ref.Provider)
}

// platformProvider is the grant discriminator of a platform agent:
// "<core>:<provider>", the bridge's borrowTarget shape.
func platformProvider(ref wire.PlatformAgentRef) string { return ref.Core + ":" + ref.Provider }

// hostStates reads each host's live advertisements and pending jobs once per
// request.
type hostStates struct {
	s      *Service
	ctx    context.Context
	online map[string]map[wire.PlatformAgentRef]string
	queued map[string]map[wire.PlatformAgentRef]int
}

func (s *Service) newHostStates(ctx context.Context) *hostStates {
	return &hostStates{s: s, ctx: ctx, online: map[string]map[wire.PlatformAgentRef]string{}, queued: map[string]map[wire.PlatformAgentRef]int{}}
}

func (h *hostStates) load(host string) {
	if _, ok := h.online[host]; ok {
		return
	}
	h.online[host], h.queued[host] = map[wire.PlatformAgentRef]string{}, map[wire.PlatformAgentRef]int{}
	if h.s.Bridges == nil {
		return
	}
	advertised, err := h.s.Bridges.ListBridgeAgentsForOwner(h.ctx, host, time.Now().UnixMilli())
	if err != nil {
		// A presentation read with no 500 path: this response shows the
		// host's agents offline, and the failure is logged (as for the
		// pending count below).
		slog.Warn("bnbloc shareagent: advertised agents read failed",
			"event", "bnbloc.shareagent.advertised_agents_failed", "host", host, "error", err)
		return
	}
	h.online[host] = shareagent.OnlineAgents(advertised)
	counts, err := h.s.Bridges.PendingAgentJobs(h.ctx, host)
	if err != nil {
		// The listing stays right about who is online; queued reads 0.
		slog.Warn("bnbloc shareagent: pending job count failed",
			"event", "bnbloc.shareagent.pending_jobs_failed", "host", host, "error", err)
		return
	}
	for _, c := range counts {
		h.queued[host][wire.PlatformAgentRef{Core: c.Core, Provider: c.Provider}] = c.Count
	}
}

// state projects one host of an agent: online, queued, targetId, default model.
func (h *hostStates) state(host string, ref wire.PlatformAgentRef, defaultModel string) wire.PlatformAgentHostState {
	h.load(host)
	target, up := h.online[host][ref]
	return wire.PlatformAgentHostState{HostUserID: host, Online: up, Queued: h.queued[host][ref], TargetID: target, DefaultModel: defaultModel}
}

// agentView assembles a platform agent from its hosts' states.
func agentView(ref wire.PlatformAgentRef, hosts []wire.PlatformAgentHostState) wire.PlatformAgent {
	v := wire.PlatformAgent{Core: ref.Core, Provider: ref.Provider, Hosts: hosts}
	for _, h := range hosts {
		v.Online = v.Online || h.Online
	}
	return v
}

// handleListPlatformAgents is GET /v1/platform-agents: every published
// platform agent, one entry per (core, provider) with the state of each host
// publishing it (empty when no host publishes anything).
func (s *Service) handleListPlatformAgents(w http.ResponseWriter, r *http.Request) {
	if !s.platformAgentsReady(w) {
		return
	}
	if callerSubject(r) == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	p, ok := s.platformPolicy(w, r)
	if !ok {
		return
	}
	states := s.newHostStates(r.Context())
	agents := []wire.PlatformAgent{}
	for _, a := range p.Published() {
		hosts := make([]wire.PlatformAgentHostState, 0, len(a.Hosts))
		for _, h := range a.Hosts {
			hosts = append(hosts, states.state(h.HostUserID, a.Ref, h.Spec.DefaultModel))
		}
		agents = append(agents, agentView(a.Ref, hosts))
	}
	httpx.WriteJSON(w, http.StatusOK, wire.PlatformAgentsReply{Agents: agents})
}

// roomPlatformAgents groups the room's available platform grants into one
// entry per agent with the state of each host serving the room, oldest
// added first. AddedBy/AddedAtMs come from the agent's earliest grant.
func roomPlatformAgents(pgs []shareagent.RoomPlatformGrant, p wire.PlatformAgentsPolicy, states *hostStates) []wire.PlatformAgent {
	agents := []wire.PlatformAgent{}
	index := map[wire.PlatformAgentRef]int{}
	for _, pg := range pgs {
		if !pg.Available {
			continue
		}
		host := pg.Grant.OwnerUserID
		spec, _ := p.Agent(host, pg.Agent.Core, pg.Agent.Provider)
		i, ok := index[pg.Agent]
		if !ok {
			i = len(agents)
			index[pg.Agent] = i
			agents = append(agents, wire.PlatformAgent{Core: pg.Agent.Core, Provider: pg.Agent.Provider, AddedBy: pg.Grant.CreatedBy, AddedAtMs: pg.Grant.CreatedAtMs})
		}
		a := &agents[i]
		if pg.Grant.CreatedAtMs < a.AddedAtMs {
			a.AddedBy, a.AddedAtMs = pg.Grant.CreatedBy, pg.Grant.CreatedAtMs
		}
		st := states.state(host, pg.Agent, spec.DefaultModel)
		a.Hosts = append(a.Hosts, st)
		a.Online = a.Online || st.Online
	}
	for i := range agents {
		hosts := agents[i].Hosts
		sort.Slice(hosts, func(x, y int) bool { return hosts[x].HostUserID < hosts[y].HostUserID })
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].AddedAtMs < agents[j].AddedAtMs })
	return agents
}

// handleListRoomPlatformAgents is GET /v1/rooms/{roomId}/platform-agents
// (room members): the platform agents added to the room that some host
// still serves. It reconciles the room with the policy first: an added agent
// (one with a live grant) is extended to every host that publishes it now
// and does not serve the room yet, and each host's agent-host membership
// converges on whether it has an available agent here.
func (s *Service) handleListRoomPlatformAgents(w http.ResponseWriter, r *http.Request) {
	roomID, _, ok := s.platformRoom(w, r)
	if !ok {
		return
	}
	p, ok := s.platformPolicy(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tid := tenant.FromContext(ctx)
	pgs, err := shareagent.RoomPlatformGrants(ctx, s.Grants, p, roomID, s.Now())
	if err != nil {
		obs.ServerError(w, "list grants failed", err)
		httpx.Error(w, "list grants failed", http.StatusInternalServerError)
		return
	}
	if s.extendRoomPlatformAgents(ctx, tid, roomID, p, pgs) {
		pgs, err = shareagent.RoomPlatformGrants(ctx, s.Grants, p, roomID, s.Now())
		if err != nil {
			obs.ServerError(w, "list grants failed", err)
			httpx.Error(w, "list grants failed", http.StatusInternalServerError)
			return
		}
	}
	// host -> has an available agent here, for every host that ever had a
	// platform grant in the room (revoked ones included, so a host whose
	// agents were all removed or unpublished loses the membership).
	hosts := map[string]bool{}
	for _, pg := range pgs {
		hosts[pg.Grant.OwnerUserID] = hosts[pg.Grant.OwnerUserID] || pg.Available
	}
	for host, want := range hosts {
		s.reconcileAgentHost(ctx, tid, roomID, host, want)
	}
	httpx.WriteJSON(w, http.StatusOK, wire.PlatformAgentsReply{Agents: roomPlatformAgents(pgs, p, s.newHostStates(ctx))})
}

// extendRoomPlatformAgents gives every agent added to the room (it holds a
// live platform grant of it) a grant and membership for each host that
// publishes it now and has none live here. Best-effort: a failure is logged
// and retried by the next listing. Reports whether anything was added.
func (s *Service) extendRoomPlatformAgents(ctx context.Context, tid, roomID string, p wire.PlatformAgentsPolicy, pgs []shareagent.RoomPlatformGrant) bool {
	added := map[wire.PlatformAgentRef]store.Grant{} // agent -> its earliest live grant
	serving := map[wire.PlatformAgentRef]map[string]bool{}
	for _, pg := range pgs {
		if !pg.Live || pg.Agent.Core == "" {
			continue
		}
		if first, ok := added[pg.Agent]; !ok || pg.Grant.CreatedAtMs < first.CreatedAtMs {
			added[pg.Agent] = pg.Grant
		}
		if serving[pg.Agent] == nil {
			serving[pg.Agent] = map[string]bool{}
		}
		serving[pg.Agent][pg.Grant.OwnerUserID] = true
	}
	extended := false
	for ref, first := range added {
		for _, h := range p.HostsOf(ref) {
			if serving[ref][h.HostUserID] {
				continue
			}
			// The extension is the original adder's: it is the same agent
			// they added, now served by one more host.
			if _, err := s.putPlatformHost(ctx, tid, roomID, h.HostUserID, ref, first.CreatedBy); err != nil {
				slog.Warn("bnbloc shareagent: extending a platform agent to a new host failed",
					"event", "bnbloc.shareagent.platform_extend_failed", "room", roomID, "host", h.HostUserID,
					"core", ref.Core, "provider", ref.Provider, "error", err)
				continue
			}
			extended = true
		}
	}
	return extended
}

// putPlatformHost lets host serve ref in the room: the agent-host membership
// first (a grant without it would be claimed by a bridge that cannot read
// the room), then the platform grant (an upsert: a revoked one is revived
// with fresh usage).
func (s *Service) putPlatformHost(ctx context.Context, tid, roomID, host string, ref wire.PlatformAgentRef, createdBy string) (store.Grant, error) {
	if err := s.AgentHosts.PutAgentHost(ctx, tid, roomID, host); err != nil {
		return store.Grant{}, err
	}
	g := store.Grant{
		GrantID:       platformGrantID(ctx, roomID, host, platformProvider(ref)),
		RoomID:        roomID,
		TargetID:      shareagent.PlatformTarget(ref),
		OwnerUserID:   host,
		GranteeUserID: "*",
		Capabilities:  store.Capabilities{Consult: true, Review: true, Delegate: true, Upgrade: true},
		CreatedAtMs:   s.Now(),
		CreatedBy:     createdBy,
		Source:        store.GrantSourcePlatform,
	}
	return g, s.Grants.PutGrant(ctx, g)
}

// reconcileAgentHost converges host's agent-host membership of the room on
// want. Best-effort (the listing is still right; the next one retries); a
// human membership is never touched (the room service enforces it too).
func (s *Service) reconcileAgentHost(ctx context.Context, tid, roomID, host string, want bool) {
	m, member := s.Membership.Lookup(roomID, host)
	var err error
	switch {
	case want && !member:
		err = s.AgentHosts.PutAgentHost(ctx, tid, roomID, host)
	case !want && member && !m.IsHuman():
		err = s.AgentHosts.DeleteAgentHost(ctx, tid, roomID, host)
	}
	if err != nil {
		slog.Warn("bnbloc shareagent: agent-host membership reconcile failed",
			"event", "bnbloc.shareagent.agent_host_reconcile_failed", "room", roomID, "host", host, "want", want, "error", err)
	}
}

// platformRoom validates a room-scoped platform-agent request: configured,
// authenticated, and the caller a member of the room (any membership: the
// caller's, for the route to ask MayChat of).
func (s *Service) platformRoom(w http.ResponseWriter, r *http.Request) (string, membership.Member, bool) {
	var none membership.Member
	if !s.platformAgentsReady(w) {
		return "", none, false
	}
	if s.AgentHosts == nil {
		httpx.Unavailable(w, "platform agents")
		return "", none, false
	}
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return "", none, false
	}
	m, ok := s.requireMember(w, r, roomID, callerSubject(r), membership.AnyMember)
	if !ok {
		return "", none, false
	}
	return roomID, m, true
}

// handleAddRoomPlatformAgent is POST /v1/rooms/{roomId}/platform-agents
// {core, provider}: any member who may chat adds a published agent, for
// every host publishing it. 201 {agent} when a host was added, 200 {agent}
// when every publishing host already served the room (idempotent).
func (s *Service) handleAddRoomPlatformAgent(w http.ResponseWriter, r *http.Request) {
	roomID, m, ok := s.platformRoom(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tid := tenant.FromContext(ctx)
	caller := callerSubject(r)
	if !m.MayChat() {
		httpx.Error(w, "only a room member who may chat can add a platform agent", http.StatusForbidden)
		return
	}
	var body wire.PlatformAgentRef
	if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &body) {
		return
	}
	ref := wire.PlatformAgentRef{Core: strings.TrimSpace(body.Core), Provider: strings.TrimSpace(body.Provider)}
	p, ok := s.platformPolicy(w, r)
	if !ok {
		return
	}
	hosts := p.HostsOf(ref)
	if ref.Core == "" || ref.Provider == "" || len(hosts) == 0 {
		httpx.Error(w, errNotPublished, http.StatusNotFound)
		return
	}
	pgs, err := shareagent.RoomPlatformGrants(ctx, s.Grants, p, roomID, s.Now())
	if err != nil {
		obs.ServerError(w, "list grants failed", err)
		httpx.Error(w, "list grants failed", http.StatusInternalServerError)
		return
	}
	serving := map[string]bool{}
	for _, pg := range pgs {
		if pg.Available && pg.Agent == ref {
			serving[pg.Grant.OwnerUserID] = true
		}
	}
	created := false
	for _, h := range hosts {
		if serving[h.HostUserID] {
			continue
		}
		if _, err := s.putPlatformHost(ctx, tid, roomID, h.HostUserID, ref, caller); err != nil {
			obs.ServerError(w, "add platform agent host failed", err)
			httpx.Error(w, "add platform agent failed", http.StatusInternalServerError)
			return
		}
		created = true
	}
	pgs, err = shareagent.RoomPlatformGrants(ctx, s.Grants, p, roomID, s.Now())
	if err != nil {
		obs.ServerError(w, "list grants failed", err)
		httpx.Error(w, "list grants failed", http.StatusInternalServerError)
		return
	}
	var view wire.PlatformAgent
	for _, a := range roomPlatformAgents(pgs, p, s.newHostStates(ctx)) {
		if a.Core == ref.Core && a.Provider == ref.Provider {
			view = a
		}
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
		s.auditPlatformAgent(r, roomID, ref, "add")
	}
	httpx.WriteJSON(w, code, map[string]any{"agent": view})
}

// handleRemoveRoomPlatformAgent is DELETE
// /v1/rooms/{roomId}/platform-agents/{provider}?core=: any member who may
// chat, the room owner, or a host serving it revokes the agent's platform
// grants in the room (every host's); a host's agent-host membership goes
// once none of its agents is left here. 204, also when it was not there.
func (s *Service) handleRemoveRoomPlatformAgent(w http.ResponseWriter, r *http.Request) {
	roomID, m, ok := s.platformRoom(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tid := tenant.FromContext(ctx)
	caller := callerSubject(r)
	ref := wire.PlatformAgentRef{Core: strings.TrimSpace(r.URL.Query().Get("core")), Provider: strings.TrimSpace(r.PathValue("provider"))}
	if ref.Core == "" || ref.Provider == "" {
		httpx.Error(w, "core and provider are required", http.StatusBadRequest)
		return
	}
	// The room's live grants of this agent, whoever hosts them (policy-
	// independent: a host may have unpublished since).
	pgs, err := shareagent.RoomPlatformGrants(ctx, s.Grants, wire.PlatformAgentsPolicy{}, roomID, s.Now())
	if err != nil {
		obs.ServerError(w, "list grants failed", err)
		httpx.Error(w, "list grants failed", http.StatusInternalServerError)
		return
	}
	var targets []store.Grant
	var rest []store.Grant // the room's other live platform grants
	for _, pg := range pgs {
		switch {
		case !pg.Live:
		case pg.Agent == ref:
			targets = append(targets, pg.Grant)
		default:
			rest = append(rest, pg.Grant)
		}
	}
	if len(targets) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The room owner always may chat; a host may also remove from a room it
	// only hosts the agent in.
	isHost := false
	for _, g := range targets {
		isHost = isHost || g.OwnerUserID == caller
	}
	if !m.MayChat() && !isHost {
		httpx.Error(w, "only a room member who may chat or the host can remove a platform agent", http.StatusForbidden)
		return
	}
	for _, g := range targets {
		if err := s.Grants.RevokeGrant(ctx, g.GrantID, s.Now()); err != nil && err != store.ErrGrantNotFound {
			obs.ServerError(w, "revoke platform grant failed", err)
			httpx.Error(w, "remove platform agent failed", http.StatusInternalServerError)
			return
		}
	}
	s.auditPlatformAgent(r, roomID, ref, "remove")
	still := map[string]bool{}
	for _, g := range rest {
		still[g.OwnerUserID] = true
	}
	for _, g := range targets {
		if still[g.OwnerUserID] {
			continue
		}
		if err := s.AgentHosts.DeleteAgentHost(ctx, tid, roomID, g.OwnerUserID); err != nil {
			// The grant is gone (the agent can no longer run here); the stale
			// membership is retried by the next listing.
			slog.Warn("bnbloc shareagent: remove agent host failed",
				"event", "bnbloc.shareagent.agent_host_remove_failed", "room", roomID, "host", g.OwnerUserID, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// auditPlatformAgent records a room's platform-agent change in the room's
// tenant.
func (s *Service) auditPlatformAgent(r *http.Request, roomID string, ref wire.PlatformAgentRef, action string) {
	s.audit(r.Context(), store.ExtAuditRecord{
		Kind: wire.AuditKindPlatformAgent, Name: ref.Core + "/" + ref.Provider, Action: action, Actor: callerSubject(r),
		TenantID: tenant.FromContext(r.Context()), Detail: "room=" + roomID,
	})
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
