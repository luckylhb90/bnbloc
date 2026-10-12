package realtimehttp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A quick try (try_draft) dispatches with a staged draft: admitted only
// for a draft the parent job staged in this room before it expires, then
// pinned on the job as its expert ("Reviewer (draft)"), marked on the
// message and audited as a try. Every other dispatch guard still applies.
func TestDispatchWearsAStagedDraft(t *testing.T) {
	f := newDispatchFixture(t)
	content := store.NewMemoryContentComponents()
	f.svc.Content = content
	p := f.parent("job_builder")
	other := f.parent("job_other")
	now := time.Now().UnixMilli()
	stage := func(id, jobID, room string, expires int64) {
		t.Helper()
		if err := content.PutContentDraft(dctx(), store.ContentDraft{ID: id, TenantID: tenant.Default, RoomID: room, JobID: jobID, Requester: "maya", Author: "jack",
			Kind: contentpkg.KindExpert, Name: "reviewer", DisplayName: "Reviewer", Version: "1.0.0", ManifestJSON: json.RawMessage(`{}`),
			BundleBlobKey: "sha256/" + strings.Repeat("d", 64), SHA256: strings.Repeat("d", 64), CreatedAtMs: now, ExpiresAtMs: expires}); err != nil {
			t.Fatal(err)
		}
	}
	stage("cd_ok", p.JobID, "room-1", now+time.Hour.Milliseconds())
	stage("cd_other_job", other.JobID, "room-1", now+time.Hour.Milliseconds())
	stage("cd_other_room", p.JobID, "room-2", now+time.Hour.Milliseconds())
	stage("cd_expired", p.JobID, "room-1", now-1)

	for _, id := range []string{"cd_missing", "cd_other_job", "cd_other_room", "cd_expired"} {
		in := toBob(p.JobID)
		in.Draft = id
		if code, out := f.dispatch("jack", in); code != http.StatusBadRequest || out["code"] != wire.DispatchDraftUnavailable {
			t.Fatalf("draft %s = %d %v; want 400 %s", id, code, out, wire.DispatchDraftUnavailable)
		}
	}
	both := toBob(p.JobID)
	both.Draft, both.Expert = "cd_ok", &wire.ChatExpert{ID: "cc_x"}
	if code, out := f.dispatch("jack", both); code != http.StatusBadRequest || out["code"] != wire.DispatchInvalid {
		t.Fatalf("draft and expert = %d %v", code, out)
	}
	// The ordinary guards hold for a try too: bob's bridge does not run the parent.
	notMine := toBob(p.JobID)
	notMine.Draft = "cd_ok"
	if code, out := f.dispatch("bob", notMine); code != http.StatusForbidden || out["code"] != wire.DispatchNotParentBridge {
		t.Fatalf("try from another bridge = %d %v", code, out)
	}

	in := toBob(p.JobID)
	in.Draft = "cd_ok"
	code, out := f.dispatch("jack", in)
	if code != http.StatusOK {
		t.Fatalf("try = %d %v", code, out)
	}
	child, err := f.jobs.GetAgentRun(dctx(), out["jobId"].(string))
	if err != nil || child.ExpertID != "cd_ok" || child.ExpertVersion != "1.0.0" || child.ExpertName != "Reviewer (draft)" || child.RequesterUserID != "maya" || child.DispatchedByJobID != p.JobID {
		t.Fatalf("child = %+v, %v; want the draft pinned on maya's dispatched job", child, err)
	}
	msgs, _ := f.chat.RoomFeed(dctx(), "room-1", 0, 10)
	var payload struct {
		Expert map[string]any `json:"expert"`
	}
	_ = json.Unmarshal(msgs[len(msgs)-1].Payload, &payload)
	if payload.Expert["id"] != "cd_ok" || payload.Expert["draft"] != true {
		t.Fatalf("message expert = %v", payload.Expert)
	}
	recs, _ := f.audit.ExtAuditRecords(dctx(), wire.AuditKindContent, 10, 0)
	if len(recs) != 1 || recs[0].Action != "try" || recs[0].Actor != "maya" || !strings.Contains(recs[0].Detail, "draft Reviewer (draft) v1.0.0 on agent bob/acme/claude") {
		t.Fatalf("audit = %+v", recs)
	}
}
