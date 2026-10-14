package shareagenthttp

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// A borrow refused because the owner authorized none records the
// borrower's access request (once per room, owner, provider and
// requester), and approving it authorizes the agent in one step
// (docs/prd/2026-10-14-multi-member-rooms.md rule 9).
func TestRefusedBorrowRequestsAccess(t *testing.T) {
	svc, _ := billingService(t)
	code, out := reserveRun(t, svc, tenant.Default, "alice", "codex", "bob", "ch")
	if code != http.StatusForbidden || out["denyReason"] != shareagent.DenyOwnerNotAuthorized || out["accessRequested"] != true || out["requestId"] == "" {
		t.Fatalf("refused reserve = %d %v; want a recorded access request", code, out)
	}
	id := out["requestId"].(string)
	// A second refusal answers the same open request.
	if _, again := reserveRun(t, svc, tenant.Default, "alice", "codex", "bob", "ch"); again["requestId"] != id {
		t.Fatalf("second refusal = %v; want the open request %s", again, id)
	}
	_, list, _ := call(t, svc, http.MethodGet, "/v1/borrow/quota-requests?role=owner&status=pending", "alice", "")
	pending := list["requests"].([]any)
	if len(pending) != 1 || pending[0].(map[string]any)["kind"] != "access" || pending[0].(map[string]any)["requester"] != "bob" {
		t.Fatalf("owner's pending = %v; want bob's access request", pending)
	}
	// One step: approving authorizes the agent (no amount needed).
	code, approved, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests/"+id+"/approve", "alice", "")
	if code != http.StatusOK || approved["authorized"] != true {
		t.Fatalf("approve = %d %v", code, approved)
	}
	if code, out := reserveRun(t, svc, tenant.Default, "alice", "codex", "bob", "ch"); code != http.StatusOK || out["reserved"] != true {
		t.Fatalf("reserve after approval = %d %v", code, out)
	}
}

// A borrower may also ask for access directly when the owner has none.
func TestAccessRequestByTheBorrower(t *testing.T) {
	svc, _ := billingService(t)
	code, out, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests", "bob", `{"roomId":"room-1","owner":"alice","provider":"gemini","requestedRuns":5}`)
	if code != http.StatusCreated || out["request"].(map[string]any)["kind"] != "access" {
		t.Fatalf("create = %d %v", code, out)
	}
	id := out["request"].(map[string]any)["requestId"].(string)
	if code, approved, _ := call(t, svc, http.MethodPost, "/v1/borrow/quota-requests/"+id+"/approve", "alice", ""); code != http.StatusOK || approved["authorized"] != true {
		t.Fatalf("approve = %d %v", code, approved)
	}
	_, auth := borrowCall(t, svc, tenant.Default, "alice", "alice", "gemini", "reserve", reserveBorrowBody{Borrower: "bob"})
	if auth["reserved"] != true || auth["runQuota"] != float64(5) {
		t.Fatalf("reserve on the approved access = %v; want 5 runs authorized", auth)
	}
}
