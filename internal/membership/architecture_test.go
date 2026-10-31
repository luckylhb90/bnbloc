package membership

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// Room membership has one owner (this package): a backend checks a request
// through RoomScope (fail closed, in the room's own tenant, with the ability
// the route needs) and asks a Member IsHuman / MayChat, never re-deriving
// either. The scan fails on the constructs that re-implement it:
//   - a fail-open guard (`Membership != nil && ...`): a nil directory must
//     refuse, as RoomScope does;
//   - a direct directory read in a handler (`.Membership.Lookup(` /
//     `.Fresh(`) instead of RoomScope, outside the reads listed below;
//   - the visitor role compared outside the owners (MayChat is the rule;
//     the store and the room service, which writes roles, own the role);
//   - `membership.Read` outside the routes that read a room for a non-member
//     (a preview of a shared room, docs/prd/2026-10-22-shared-room-preview.md):
//     a route reads for a preview only by naming Read, so the list is the
//     one below and the gateway's preview list (apipath.go isPreviewRead)
//     is its edge;
//   - a directory built outside FromEnv (`NewRoomClient(` / `NewStatic()`
//     in a service): each service used to pick its own, and production
//     fell back to an empty set that silently refused every member.
func TestNoMembershipFailOpen(t *testing.T) {
	ownDirectory := regexp.MustCompile(`membership\.(NewRoomClient|NewStatic)\(`)
	failOpen := regexp.MustCompile(`Membership\s*!=\s*nil\s*&&`)
	directRead := regexp.MustCompile(`\.Membership\.(Lookup|Fresh)\(`)
	visitor := regexp.MustCompile(`\bRoleVisitor\b`)
	readNeed := regexp.MustCompile(`membership\.Read\b`)
	// The files whose routes serve a preview: realtime's history and event
	// stream (with its heartbeat re-check). The room service's reads go
	// through its own requireRead.
	readNeedAllowed := map[string]bool{"services/realtime/service.go": true}
	// A directory read that is not a request's membership check, with why.
	directReadAllowed := map[string]string{
		// A read across rooms the caller names: each room in its own tenant
		// (the unread counts; the member's activity stream).
		"services/realtime/service.go":         "unreadFor",
		"services/realtime/member_activity.go": "handleMyActivity",
		// Converging a host's agent-host membership, not authorizing a caller.
		"services/shareagent/platform_agents.go": "reconcileAgentHost",
		// The edge's one live lookup (any tenant): it decides reach and room
		// roles before the request has a room tenant; backends re-check with
		// RoomScope.
		"services/gateway/tenant_scope.go": "roomMember",
		// Who may read a channel for a background reader or a cross-room
		// listing (a notification, a run listing across rooms): the room
		// role decides which members-only sections' names they know, never
		// whether they are in the room, which the caller checked.
		"services/realtime/channel_access.go": "memberMayReadChannel",
		"services/activity/channel_access.go": "memberOf",
	}
	roleOwners := []string{"internal/store/", "internal/membership/", "services/room/"}
	var problems []string
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "core"}}, func(rel string, src []byte) {
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			at := rel + ":" + strconv.Itoa(i+1)
			if failOpen.MatchString(line) {
				problems = append(problems, at+": fail-open membership guard (use membership.RoomScope)")
			}
			if directRead.MatchString(line) && directReadAllowed[rel] == "" {
				problems = append(problems, at+": direct membership read (use membership.RoomScope)")
			}
			if readNeed.MatchString(line) && !readNeedAllowed[rel] {
				problems = append(problems, at+": membership.Read outside the preview routes (a route reads for a non-member only where listed here and in the gateway's isPreviewRead)")
			}
			if ownDirectory.MatchString(line) {
				problems = append(problems, at+": membership directory built by hand (use membership.FromEnv)")
			}
			if visitor.MatchString(line) && !hasAnyPrefix(rel, roleOwners) {
				problems = append(problems, at+": visitor role compared (use membership.Member.MayChat)")
			}
		}
	})
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("room membership re-derived outside internal/membership:\n  %s", strings.Join(problems, "\n  "))
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
