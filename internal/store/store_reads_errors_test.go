package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// readsWithoutError are the store interface operations that still return a
// result with no error. A read that cannot report a failure turns a database
// blip into a wrong answer ("not a member", "no usage", "no rows"), which is
// how a failed membership read once re-provisioned a subject and rewrote
// their org role. This list only shrinks: a new store operation returns an
// error, and an entry whose method gained one must be removed (the test fails
// on a stale entry). Batch 4.2 of the code-health plan empties it.
var readsWithoutError = map[string]bool{
	"ActivityStore.SubmitQueryable":           true,
	"ActivityStore.SyncStates":                true,
	"AgentPrefsStore.GetAgentPrefs":           true,
	"AgentUsageReader.AgentUsage":             true,
	"ArtifactStore.ListArtifactVersions":      true,
	"BorrowUsageStore.BorrowTurns":            true,
	"BridgeJobStore.ListAgentJobs":            true,
	"BridgeJobStore.ListBridgeAgents":         true,
	"BridgeJobStore.ListBridgeAgentsForOwner": true,
	"ChannelStore.DefaultChannel":             true,
	"ChannelStore.GetChannel":                 true,
	"ChannelStore.ListChannels":               true,
	"ChatStore.LookupClientMsg":               true,
	"GrantStore.AuditRecords":                 true,
	"GrantStore.RoomGrants":                   true,
	"GrantStore.TenantAuditRecords":           true,
	"QuotaRequestStore.ListQuotaRequests":     true,
	"RoomStore.AgentHostRoomsAllTenants":      true,
	"RoomStore.GetMember":                     true,
	"RoomStore.IsMember":                      true,
	"RoomStore.MemberCount":                   true,
	"RoomStore.MemberRole":                    true,
	"RoomStore.Members":                       true,
	"RoomStore.MembershipAnyTenant":           true,
	"RoomStore.RoomTenant":                    true,
	"RoomStore.RoomsForUser":                  true,
	"RoomStore.RoomsForUserAllTenants":        true,
	"RoomStore.RoomsForUserDetailed":          true,
	"TeamAgentReader.TenantTeamAgents":        true,
	"TeamAgentReader.TenantTeamJobs":          true,
	"TenantStore.GetJoinRequest":              true,
	"TenantStore.ListJoinRequests":            true,
	"TenantStore.ListJoinRequestsForUser":     true,
	"TenantStore.ListMembers":                 true,
	"TenantStore.ListMembershipsForUser":      true,
	"TenantStore.ListTenants":                 true,
	"TenantStore.PrimaryTenantForUser":        true,
}

// TestStoreReadsReturnErrors fails when a store interface operation (a method
// taking a context) returns results without an error, unless it is on the
// shrinking allowlist above, and when an allowlisted method no longer exists
// in that shape.
func TestStoreReadsReturnErrors(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := map[string]bool{}
	var offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			it, ok := ts.Type.(*ast.InterfaceType)
			if !ok || !ts.Name.IsExported() {
				return true
			}
			for _, m := range it.Methods.List {
				ft, ok := m.Type.(*ast.FuncType)
				if !ok || len(m.Names) == 0 || !takesContext(ft) || ft.Results == nil || returnsError(ft) {
					continue
				}
				key := ts.Name.Name + "." + m.Names[0].Name
				found[key] = true
				if !readsWithoutError[key] {
					offenders = append(offenders, fmt.Sprintf("%s (%s)", key, fset.Position(m.Pos())))
				}
			}
			return true
		})
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("store operations must return an error (a failed read is never an empty answer):\n  %s", strings.Join(offenders, "\n  "))
	}
	var stale []string
	for k := range readsWithoutError {
		if !found[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("readsWithoutError entries that now return an error (or are gone); remove them so the list only shrinks:\n  %s", strings.Join(stale, "\n  "))
	}
}

func takesContext(ft *ast.FuncType) bool {
	if ft.Params == nil || len(ft.Params.List) == 0 {
		return false
	}
	sel, ok := ft.Params.List[0].Type.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Context"
}

func returnsError(ft *ast.FuncType) bool {
	for _, r := range ft.Results.List {
		if id, ok := r.Type.(*ast.Ident); ok && id.Name == "error" {
			return true
		}
	}
	return false
}
