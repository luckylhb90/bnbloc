package membership

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
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
//   - a directory built outside FromEnv (`NewRoomClient(` / `NewStatic()`
//     in a service): each service used to pick its own, and production
//     fell back to an empty set that silently refused every member.
func TestNoMembershipFailOpen(t *testing.T) {
	root := filepath.Join("..", "..")
	ownDirectory := regexp.MustCompile(`membership\.(NewRoomClient|NewStatic)\(`)
	failOpen := regexp.MustCompile(`Membership\s*!=\s*nil\s*&&`)
	directRead := regexp.MustCompile(`\.Membership\.(Lookup|Fresh)\(`)
	visitor := regexp.MustCompile(`\bRoleVisitor\b`)
	// A directory read that is not a request's membership check, with why.
	directReadAllowed := map[string]string{
		// A read across rooms the caller names: each room in its own tenant.
		"services/realtime/service.go": "handleUnread",
		// Converging a host's agent-host membership, not authorizing a caller.
		"services/shareagent/platform_agents.go": "reconcileAgentHost",
		// The edge's one live lookup (any tenant): it decides reach and room
		// roles before the request has a room tenant; backends re-check with
		// RoomScope.
		"services/gateway/tenant_scope.go": "roomMember",
	}
	roleOwners := []string{"internal/store/", "internal/membership/", "services/room/"}
	var problems []string
	for _, dir := range []string{"services", "internal", "core"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(raw), "\n") {
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
				if ownDirectory.MatchString(line) {
					problems = append(problems, at+": membership directory built by hand (use membership.FromEnv)")
				}
				if visitor.MatchString(line) && !hasAnyPrefix(rel, roleOwners) {
					problems = append(problems, at+": visitor role compared (use membership.Member.MayChat)")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
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
