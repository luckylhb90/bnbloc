package realtimehttp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A team lead's dispatch through a role: maya asked jack's agent (the lead,
// borrowed) to run her team; the lead staffs the "reviewer" role on bob's
// agent wearing the role's expert. The dispatch is still maya's request in
// every respect (author, the job's requester: bob's bridge reserves the
// borrow for her), carries the role's expert pinned on the job, and is
// labeled with the role on the message and in the audit.
func TestDispatchThroughATeamRole(t *testing.T) {
	f := newDispatchFixture(t)
	content := store.NewMemoryContentComponents()
	man := json.RawMessage(`{"kind":"` + contentpkg.KindExpert + `","name":"code-reviewer","version":"1.2.0","displayName":"Code reviewer"}`)
	if err := content.CreateContentComponent(dctx(), store.ContentComponent{ID: "cc_rev", Kind: contentpkg.KindExpert, Name: "code-reviewer", TenantID: tenant.Default, Owner: "maya", Scope: store.ContentScopePersonal, Status: store.ContentStatusActive},
		store.ContentComponentVersion{Version: "1.2.0", ManifestJSON: man, BundleBlobKey: "sha256/rev"}); err != nil {
		t.Fatal(err)
	}
	f.svc.Content = content
	p := f.parent("job_lead", func(j *store.AgentJob) {
		j.ExpertID, j.ExpertVersion, j.ExpertName = "cc_team", "1.0.0", "Release team"
	})

	in := toBob(p.JobID)
	in.Expert = &wire.ChatExpert{ID: "cc_rev", Version: "1.2.0"}
	in.Role = "reviewer"
	code, out := f.dispatch("jack", in)
	if code != http.StatusOK {
		t.Fatalf("role dispatch = %d %v", code, out)
	}
	child, err := f.jobs.GetAgentRun(dctx(), out["jobId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if child.RequesterUserID != "maya" || child.OwnerUserID != "bob" || child.Provider != "claude" || child.ExpertID != "cc_rev" || child.ExpertVersion != "1.2.0" || child.DispatchedByJobID != p.JobID {
		t.Fatalf("child = %+v; want maya's request to bob/claude wearing the reviewer's expert", child)
	}
	msgs, _ := f.chat.RoomFeed(dctx(), "room-1", 0, 10)
	var payload struct {
		UserID       string              `json:"userId"`
		DispatchedBy wire.DispatchMarker `json:"dispatchedBy"`
		Expert       struct {
			ID string `json:"id"`
		} `json:"expert"`
	}
	if len(msgs) != 1 || json.Unmarshal(msgs[0].Payload, &payload) != nil {
		t.Fatalf("feed = %v", msgs)
	}
	if payload.UserID != "maya" || payload.DispatchedBy.Role != "reviewer" || payload.DispatchedBy.Via != "jack/codex" || payload.Expert.ID != "cc_rev" {
		t.Fatalf("payload = %+v", payload)
	}
	recs, _ := f.audit.ExtAuditRecords(dctx(), AgentDispatchAuditKind, 10, 0)
	if len(recs) != 1 || !strings.Contains(recs[0].Detail, "role=reviewer") || !strings.Contains(recs[0].Detail, "requester=maya") {
		t.Fatalf("audit = %+v", recs)
	}

	// A malformed role is refused before anything is posted.
	in.Role, in.ClientMsgID = "Not A Role", "r2"
	if code, out := f.dispatch("jack", in); code != http.StatusBadRequest || out["code"] != wire.DispatchInvalid {
		t.Fatalf("bad role = %d %v", code, out)
	}
}
