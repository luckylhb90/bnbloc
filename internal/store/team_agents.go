package store

import (
	"context"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Team dashboard read side (F1): a tenant-wide, read-only view over data the
// bridge/job/activity paths already persist. Nothing here writes.

// TeamAgent is one agent a bridge advertised in the ctx tenant, joined to that
// bridge's liveness. Unlike ListBridgeAgents it keeps offline agents (with the
// last time their bridge was seen), because the dashboard shows them.
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
	// tenant (ownerUserID "" = all owners), online when its bridge heartbeated
	// within BridgeOfflineAfterMs of nowMs.
	TenantTeamAgents(ctx context.Context, ownerUserID string, nowMs int64) []TeamAgent
	// TenantTeamJobs returns the ctx tenant's jobs created at/after sinceMs plus
	// every still-active job regardless of age (ownerUserID "" = all owners),
	// newest-first, at most MaxTeamJobs.
	TenantTeamJobs(ctx context.Context, ownerUserID string, sinceMs int64) []AgentJob
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
	// created at/after sinceMs (ownerUserID "" = all owners).
	AgentUsage(ctx context.Context, ownerUserID string, sinceMs int64) []AgentTurnUsage
}

var (
	_ TeamAgentReader  = (*MemoryBridgeJobs)(nil)
	_ AgentUsageReader = (*Memory)(nil)
)

// TenantTeamAgents implements TeamAgentReader for the memory store.
func (s *MemoryBridgeJobs) TenantTeamAgents(ctx context.Context, ownerUserID string, nowMs int64) []TeamAgent {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TeamAgent
	for bridgeID, byAgent := range s.agents {
		hb, hasHB := s.heartbeats[bridgeID]
		for _, a := range byAgent {
			if a.TenantID != tid || a.Disabled {
				continue
			}
			if ownerUserID != "" && !strings.EqualFold(a.OwnerUserID, ownerUserID) {
				continue
			}
			seen := a.LastSeenAtMs
			online := false
			if hasHB {
				seen = hb.LastSeenAtMs
				online = hb.Status != BridgeOffline && hb.Status != BridgeDraining && hb.LastSeenAtMs >= nowMs-BridgeOfflineAfterMs
			}
			out = append(out, TeamAgent{OwnerUserID: a.OwnerUserID, Core: a.Core, Provider: a.Provider, TargetID: a.TargetID, Online: online, LastSeenAtMs: seen})
		}
	}
	return out
}

// TenantTeamJobs implements TeamAgentReader for the memory store.
func (s *MemoryBridgeJobs) TenantTeamJobs(ctx context.Context, ownerUserID string, sinceMs int64) []AgentJob {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AgentJob
	for _, j := range s.jobs {
		if j.TenantID != tid {
			continue
		}
		if ownerUserID != "" && !strings.EqualFold(j.OwnerUserID, ownerUserID) {
			continue
		}
		if j.CreatedAtMs < sinceMs && j.Status.Terminal() {
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAtMs > out[k].CreatedAtMs })
	if len(out) > MaxTeamJobs {
		out = out[:MaxTeamJobs]
	}
	return out
}

// usageSample is one reported reply's metrics, kept by the memory activity
// store so AgentUsage matches the MySQL aggregation over agent_messages.
type usageSample struct {
	tenant      string
	owner       string
	provider    string
	createdAtMs int64
	metrics     map[string]any
}

// replyUsage extracts the attribution and reported metrics of one agent reply
// payload (the bridge's room-record shape). ok=false when the reply carries no
// reported metrics.
func replyUsage(payload any) (owner, provider string, metrics map[string]any, ok bool) {
	p, isMap := payload.(map[string]any)
	if !isMap {
		return "", "", nil, false
	}
	m, isMap := p["metrics"].(map[string]any)
	if !isMap || !wire.TurnUsageFromMetrics(m).Reported {
		return "", "", nil, false
	}
	owner, _ = p["ownerUserId"].(string)
	provider, _ = p["provider"].(string)
	return owner, provider, m, true
}

// recordUsageLocked keeps the reported metrics of genuinely new replies.
// Caller holds m.mu.
func (m *Memory) recordUsageLocked(tnt string, fresh []wire.Message) {
	for _, msg := range fresh {
		owner, provider, metrics, ok := replyUsage(msg.Payload)
		if !ok {
			continue
		}
		m.usage = append(m.usage, usageSample{tenant: tnt, owner: owner, provider: provider, createdAtMs: msg.CreatedAtUnixMs, metrics: metrics})
	}
}

// AgentUsage implements AgentUsageReader for the memory store.
func (m *Memory) AgentUsage(ctx context.Context, ownerUserID string, sinceMs int64) []AgentTurnUsage {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	byKey := map[string]*AgentTurnUsage{}
	var order []string
	for _, s := range m.usage {
		if s.tenant != tnt || s.createdAtMs < sinceMs {
			continue
		}
		if ownerUserID != "" && !strings.EqualFold(s.owner, ownerUserID) {
			continue
		}
		key := s.owner + "\x00" + s.provider
		u := byKey[key]
		if u == nil {
			u = &AgentTurnUsage{OwnerUserID: s.owner, Provider: s.provider}
			byKey[key] = u
			order = append(order, key)
		}
		tu := wire.TurnUsageFromMetrics(s.metrics)
		u.ReportedTurns++
		u.InputTokens += tu.InputTokens
		u.OutputTokens += tu.OutputTokens
		if tu.CostUsd != nil {
			u.CostUsd += *tu.CostUsd
			u.CostReportedBy++
		}
	}
	out := make([]AgentTurnUsage, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
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
