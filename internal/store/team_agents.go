package store

import (
	"context"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Team dashboard read side (F1): a tenant-wide, read-only view over data the
// bridge/job/activity paths already persist. Nothing here writes.

// TeamAgent is one agent a bridge advertised in the ctx tenant, joined to that
// bridge's liveness. Unlike ListBridgeAgentsForOwner it keeps offline agents
// (with the last time their bridge was seen), because the dashboard shows
// them.
type TeamAgent struct {
	OwnerUserID  string
	Core         string
	Provider     string
	TargetID     string
	Online       bool
	LastSeenAtMs int64
}

// MaxTeamJobs bounds one TenantTeamJobs scan.
const MaxTeamJobs = 5_000

// TeamAgentReader is the bridge-job store's tenant-wide dashboard read side.
type TeamAgentReader interface {
	// TenantTeamAgents returns every non-disabled agent advertised in the ctx
	// tenant (ownerUserID "" = all owners), online when its bridge is live at
	// nowMs (BridgeLive), ordered by (owner, core, provider, target id,
	// bridge id).
	TenantTeamAgents(ctx context.Context, ownerUserID string, nowMs int64) ([]TeamAgent, error)
	// TenantTeamJobs returns the ctx tenant's jobs created at/after sinceMs plus
	// every still-active job regardless of age (ownerUserID "" = all owners),
	// newest first (created_at_ms, then job id, descending), at most
	// MaxTeamJobs.
	TenantTeamJobs(ctx context.Context, ownerUserID string, sinceMs int64) ([]AgentJob, error)
}

// AgentTurnUsage aggregates the turn metrics agents reported on their replies
// (the room record's payload.metrics: inputTokens/outputTokens/costUsd) for one
// owner's provider. Only replies whose metrics are reported count.
type AgentTurnUsage struct {
	OwnerUserID    string
	Provider       string
	ReportedTurns  int
	InputTokens    int64
	OutputTokens   int64
	CostUsd        float64
	CostReportedBy int // replies that carried a costUsd
}

// AgentUsageReader is the activity store's reported-metrics aggregation.
type AgentUsageReader interface {
	// AgentUsage sums reported reply metrics in the ctx tenant for replies
	// created at/after sinceMs (ownerUserID "" = all owners), one row per
	// (owner, provider) in that order.
	AgentUsage(ctx context.Context, ownerUserID string, sinceMs int64) ([]AgentTurnUsage, error)
}

var (
	_ TeamAgentReader  = (*MemoryBridgeJobs)(nil)
	_ AgentUsageReader = (*Memory)(nil)
)

// TenantTeamAgents implements TeamAgentReader for the memory store.
func (s *MemoryBridgeJobs) TenantTeamAgents(ctx context.Context, ownerUserID string, nowMs int64) ([]TeamAgent, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ads []BridgeAgent
	for _, byAgent := range s.agents {
		for _, a := range byAgent {
			if a.TenantID == tid && !a.Disabled && (ownerUserID == "" || a.OwnerUserID == ownerUserID) {
				ads = append(ads, a)
			}
		}
	}
	sortBridgeAgents(ads)
	out := make([]TeamAgent, 0, len(ads))
	for _, a := range ads {
		seen, online := a.LastSeenAtMs, false
		if hb, ok := s.heartbeats[a.BridgeID]; ok {
			seen, online = hb.LastSeenAtMs, BridgeLive(hb, nowMs)
		}
		out = append(out, TeamAgent{OwnerUserID: a.OwnerUserID, Core: a.Core, Provider: a.Provider, TargetID: a.TargetID, Online: online, LastSeenAtMs: seen})
	}
	return out, nil
}

// TenantTeamJobs implements TeamAgentReader for the memory store.
func (s *MemoryBridgeJobs) TenantTeamJobs(ctx context.Context, ownerUserID string, sinceMs int64) ([]AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []AgentJob{}
	for _, j := range s.jobs {
		if j.TenantID != tid {
			continue
		}
		if ownerUserID != "" && j.OwnerUserID != ownerUserID {
			continue
		}
		if j.CreatedAtMs < sinceMs && j.Status.Terminal() {
			continue
		}
		out = append(out, j)
	}
	sortJobsNewestFirst(out)
	if len(out) > MaxTeamJobs {
		out = out[:MaxTeamJobs]
	}
	return out, nil
}

// usageSample is one reported reply's normalized usage, kept by the memory
// activity store as the MySQL store keeps it in agent_messages' usage columns.
type usageSample struct {
	tenant      string
	room        string
	usage       ReplyUsage
	createdAtMs int64
}

// ReplyUsage is the usage one agent reply reported, attributed to its owner
// and provider. It is read once, when the reply is appended (ReplyUsageOf),
// and stored normalized: negative counts and costs are dropped, so no
// reporter can lower the owner's or the organization's burn.
type ReplyUsage struct {
	Owner, Provider string
	wire.TurnUsage
}

// ReplyUsageOf is the one reader of a reply's reported usage: the attribution
// and wire.TurnUsageFromMetrics of an agent reply payload (the bridge's room
// record). ok=false when the reply reports no usage.
func ReplyUsageOf(payload any) (ReplyUsage, bool) {
	p, isMap := payload.(map[string]any)
	if !isMap {
		return ReplyUsage{}, false
	}
	m, isMap := p["metrics"].(map[string]any)
	if !isMap {
		return ReplyUsage{}, false
	}
	u := wire.TurnUsageFromMetrics(m)
	if !u.Reported {
		return ReplyUsage{}, false
	}
	owner, _ := p["ownerUserId"].(string)
	provider, _ := p["provider"].(string)
	return ReplyUsage{Owner: owner, Provider: provider, TurnUsage: u}, true
}

// recordUsageLocked keeps the normalized usage of genuinely new replies.
// Caller holds m.mu.
func (m *Memory) recordUsageLocked(tnt, room string, fresh []wire.Message) {
	for _, msg := range fresh {
		if u, ok := ReplyUsageOf(msg.Payload); ok {
			m.usage = append(m.usage, usageSample{tenant: tnt, room: room, usage: u, createdAtMs: msg.CreatedAtUnixMs})
		}
	}
}

// AgentUsage implements AgentUsageReader for the memory store.
func (m *Memory) AgentUsage(ctx context.Context, ownerUserID string, sinceMs int64) ([]AgentTurnUsage, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	byKey := map[[2]string]*AgentTurnUsage{}
	for _, s := range m.usage {
		if s.tenant != tnt || s.createdAtMs < sinceMs {
			continue
		}
		if ownerUserID != "" && s.usage.Owner != ownerUserID {
			continue
		}
		key := [2]string{s.usage.Owner, s.usage.Provider}
		u := byKey[key]
		if u == nil {
			u = &AgentTurnUsage{OwnerUserID: s.usage.Owner, Provider: s.usage.Provider}
			byKey[key] = u
		}
		tu := s.usage.TurnUsage
		u.ReportedTurns++
		u.InputTokens += tu.InputTokens
		u.OutputTokens += tu.OutputTokens
		if tu.CostUsd != nil {
			u.CostUsd += *tu.CostUsd
			u.CostReportedBy++
		}
	}
	out := make([]AgentTurnUsage, 0, len(byKey))
	for _, u := range byKey {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].OwnerUserID != out[k].OwnerUserID {
			return out[i].OwnerUserID < out[k].OwnerUserID
		}
		return out[i].Provider < out[k].Provider
	})
	return out, nil
}

// TeamAgents returns the bridge-job store's dashboard read side (nil when the
// configured store does not provide one).
func (b *Backends) TeamAgents() TeamAgentReader {
	r, _ := b.BridgeJobs.(TeamAgentReader)
	return r
}

// AgentUsage returns the activity store's reported-metrics aggregation (nil
// when the configured store does not provide one).
func (b *Backends) AgentUsage() AgentUsageReader {
	r, _ := b.Activity.(AgentUsageReader)
	return r
}
