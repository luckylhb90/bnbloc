package shareagenthttp

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Platform-agent usage (docs/prd/2026-10-10-platform-agents-v2-onboarding.md
// §2): the super admin's dashboard over the borrow accountability rows of
// platform grants — the same recorded borrow turns the usage reports read
// (store.BorrowTurns), narrowed to platform grants and taken across every
// organization (platform agents serve all of them).

const (
	platformUsageTop    = 5
	platformUsageRecent = 20
)

// handlePlatformAgentsUsage is GET /v1/platform/platform-agents/usage
// (manage_platform at the gateway). Query: tzOffsetMin (as the usage
// reports; default UTC) places "today" and "this month".
func (s *Service) handlePlatformAgentsUsage(w http.ResponseWriter, r *http.Request) {
	if callerSubject(r) == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	loc, bad := reportLocation(r.URL.Query().Get("tzOffsetMin"))
	if bad != "" {
		writeErr(w, http.StatusBadRequest, bad)
		return
	}
	nowMs := s.Now()
	now := time.UnixMilli(nowMs).In(loc)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).UnixMilli()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UnixMilli()
	turns := s.Grants.BorrowTurns(r.Context(), store.BorrowTurnQuery{
		FromMs: monthStart, ToMs: nowMs + 1, PlatformOnly: true, AllTenants: true,
	})
	writeJSON(w, http.StatusOK, s.platformUsage(r.Context(), turns, dayStart))
}

// usageAcc accumulates runs, tokens and cost.
type usageAcc struct {
	runs   int
	tokens int64
	cost   float64
}

func (a *usageAcc) add(t store.AuditRecord) {
	a.runs++
	a.tokens += turnTokens(t)
	if t.Cost != nil {
		a.cost += *t.Cost
	}
}

// turnTokens is a turn's reported input + output tokens (0 when unreported).
func turnTokens(t store.AuditRecord) int64 {
	var n int64
	if t.InputTokens != nil {
		n += *t.InputTokens
	}
	if t.OutputTokens != nil {
		n += *t.OutputTokens
	}
	return n
}

// periodTotals sums turns into one period's totals.
type periodTotals struct {
	usageAcc
	members map[string]bool
	rooms   map[string]bool
}

func newPeriodTotals() *periodTotals {
	return &periodTotals{members: map[string]bool{}, rooms: map[string]bool{}}
}

func (p *periodTotals) add(t store.AuditRecord) {
	p.usageAcc.add(t)
	p.members[t.ActorUserID] = true
	p.rooms[t.TenantID+"\x00"+t.RoomID] = true
}

func (p *periodTotals) wire() wire.PlatformAgentUsageTotals {
	return wire.PlatformAgentUsageTotals{Runs: p.runs, Members: len(p.members), Rooms: len(p.rooms), Tokens: p.tokens, Cost: p.cost}
}

// platformUsage aggregates the month's platform-agent turns (oldest first).
func (s *Service) platformUsage(ctx context.Context, turns []store.AuditRecord, dayStartMs int64) wire.PlatformAgentUsageReply {
	month, today := newPeriodTotals(), newPeriodTotals()
	members := map[string]*usageAcc{}
	memberIDs := map[string]string{} // lower -> first-seen spelling
	type roomKey struct{ tenant, room string }
	rooms := map[roomKey]*usageAcc{}
	for _, t := range turns {
		month.add(t)
		if t.AtMs >= dayStartMs {
			today.add(t)
		}
		mk := t.ActorUserID
		if members[mk] == nil {
			members[mk], memberIDs[mk] = &usageAcc{}, t.ActorUserID
		}
		members[mk].add(t)
		rk := roomKey{t.TenantID, t.RoomID}
		if rooms[rk] == nil {
			rooms[rk] = &usageAcc{}
		}
		rooms[rk].add(t)
	}
	names := s.usageRoomNames(ctx)
	out := wire.PlatformAgentUsageReply{
		Today: today.wire(), Month: month.wire(),
		TopMembers: []wire.PlatformAgentUsageMember{}, TopRooms: []wire.PlatformAgentUsageRoom{}, Recent: []wire.PlatformAgentUsageRun{},
		Truncated: len(turns) >= store.MaxBorrowTurnRows,
	}
	for k, a := range members {
		out.TopMembers = append(out.TopMembers, wire.PlatformAgentUsageMember{MemberID: memberIDs[k], Runs: a.runs, Tokens: a.tokens, Cost: a.cost})
	}
	sort.Slice(out.TopMembers, func(i, j int) bool {
		a, b := out.TopMembers[i], out.TopMembers[j]
		if a.Runs != b.Runs {
			return a.Runs > b.Runs
		}
		if a.Cost != b.Cost {
			return a.Cost > b.Cost
		}
		return a.MemberID < b.MemberID
	})
	out.TopMembers = out.TopMembers[:min(len(out.TopMembers), platformUsageTop)]
	for k, a := range rooms {
		out.TopRooms = append(out.TopRooms, wire.PlatformAgentUsageRoom{RoomID: k.room, TenantID: k.tenant, Runs: a.runs, Tokens: a.tokens, Cost: a.cost})
	}
	sort.Slice(out.TopRooms, func(i, j int) bool {
		a, b := out.TopRooms[i], out.TopRooms[j]
		if a.Runs != b.Runs {
			return a.Runs > b.Runs
		}
		if a.Cost != b.Cost {
			return a.Cost > b.Cost
		}
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		return a.RoomID < b.RoomID
	})
	out.TopRooms = out.TopRooms[:min(len(out.TopRooms), platformUsageTop)]
	for i := range out.TopRooms {
		out.TopRooms[i].RoomName = names(out.TopRooms[i].TenantID, out.TopRooms[i].RoomID)
	}
	agents := s.usageAgents(ctx)
	for i := len(turns) - 1; i >= 0 && len(out.Recent) < platformUsageRecent; i-- {
		t := turns[i]
		run := wire.PlatformAgentUsageRun{
			AtMs: t.AtMs, MemberID: t.ActorUserID, RoomID: t.RoomID, RoomName: names(t.TenantID, t.RoomID),
			TenantID: t.TenantID, Agent: agents(t.TenantID, t.GrantID), HostUserID: t.OwnerUserID, Model: t.Model,
		}
		if t.Cost != nil {
			run.Cost = *t.Cost
		}
		out.Recent = append(out.Recent, run)
	}
	return out
}

// usageRoomNames resolves room names in their own tenant, once per room.
func (s *Service) usageRoomNames(ctx context.Context) func(tenantID, roomID string) string {
	cache := map[string]string{}
	return func(tenantID, roomID string) string {
		k := tenantID + "\x00" + roomID
		if v, ok := cache[k]; ok {
			return v
		}
		v := ""
		if s.RoomName != nil {
			v = s.RoomName(tenant.NewContext(ctx, tenantID), roomID)
		}
		cache[k] = v
		return v
	}
}

// usageAgents resolves a turn's agent ("core/provider") from its platform
// grant, once per grant.
func (s *Service) usageAgents(ctx context.Context) func(tenantID, grantID string) string {
	cache := map[string]string{}
	return func(tenantID, grantID string) string {
		if v, ok := cache[grantID]; ok {
			return v
		}
		v := ""
		if g, err := s.Grants.GetGrant(tenant.NewContext(ctx, tenantID), grantID); err == nil {
			if ref, ok := shareagent.PlatformAgentOfTarget(g.TargetID); ok {
				v = ref.Core + "/" + ref.Provider
			}
		}
		cache[grantID] = v
		return v
	}
}
