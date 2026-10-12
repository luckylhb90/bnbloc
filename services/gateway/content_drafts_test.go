package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A quick try's draft: staged only by the bridge running a job (as its
// requester, validated like a save), never listed in a Library, worn only
// by a job that job dispatches (its effective expert, its bundle granted to
// that job's bridge), and no longer once it expires.
func TestContentDraftStagedWornAndExpired(t *testing.T) {
	g := newContentGateway(t)
	ctx := tenant.NewContext(context.Background(), "home")
	now := time.Now().UnixMilli()
	for _, b := range []struct{ id, owner string }{{"br-jack", "jack"}, {"br-bob", "bob"}} {
		if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: b.id, OwnerUserID: b.owner, Status: store.BridgeOnline, LastSeenAtMs: now}); err != nil {
			t.Fatal(err)
		}
	}
	// maya asked jack's agent (the builder), which stages the draft.
	if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-builder", RoomID: "room-1", MessageSeq: 5, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "opencode",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	tarCT := "application/x-tar"
	stage := "/v1/content-drafts?job=job-builder&room=room-1"

	if code, raw := g.do("jack", http.MethodPost, "/v1/content-drafts", tarCT, packageTar(t, draftExpert("reviewer", nil))); code != http.StatusBadRequest {
		t.Fatalf("stage without a job = %d %s", code, raw)
	}
	if code, raw := g.do("bob", http.MethodPost, stage, tarCT, packageTar(t, draftExpert("reviewer", nil))); code != http.StatusForbidden {
		t.Fatalf("stage from another bridge = %d %s", code, raw)
	}
	bad := draftExpert("reviewer", nil)
	delete(bad, "INSTRUCTIONS.md")
	if code, raw := g.do("jack", http.MethodPost, stage, tarCT, packageTar(t, bad)); code != http.StatusBadRequest || !strings.Contains(string(raw), "INSTRUCTIONS.md") {
		t.Fatalf("invalid draft = %d %s", code, raw)
	}
	prompts := map[string]string{"component.json": `{"kind":"bnbloc.prompt.package/v1","name":"p","version":"1.0.0","displayName":"P","prompts":[{"id":"a","title":"A","body":"hi"}]}`}
	if code, raw := g.do("jack", http.MethodPost, stage, tarCT, packageTar(t, prompts)); code != http.StatusBadRequest {
		t.Fatalf("prompt pack draft = %d %s", code, raw)
	}
	code, raw := g.do("jack", http.MethodPost, stage, tarCT, packageTar(t, draftExpert("reviewer", nil)))
	var d wire.ContentDraft
	if code != http.StatusCreated || json.Unmarshal(raw, &d) != nil || !strings.HasPrefix(d.ID, store.ContentDraftIDPrefix) || d.Kind != "expert" || d.Name != "reviewer" || d.ExpiresAtMs <= now {
		t.Fatalf("stage = %d %s", code, raw)
	}
	stored, err := g.svc.ContentComponents.GetContentDraft(ctx, d.ID)
	if err != nil || stored.Requester != "maya" || stored.Author != "jack" || stored.JobID != "job-builder" || stored.RoomID != "room-1" || stored.TenantID != "home" {
		t.Fatalf("stored draft = %+v, %v", stored, err)
	}

	// Never listed: not in maya's Library (nor jack's), not readable as a component.
	for _, who := range []string{"maya", "jack"} {
		if _, raw := g.do(who, http.MethodGet, "/v1/content-components?kind=expert", "", nil); strings.Contains(string(raw), d.ID) || strings.Contains(string(raw), `"reviewer"`) {
			t.Fatalf("%s's Library lists the draft: %s", who, raw)
		}
	}
	if code, _ := g.do("maya", http.MethodGet, "/v1/content-components/"+d.ID, "", nil); code != http.StatusNotFound {
		t.Fatalf("draft read as a component = %d", code)
	}

	// The dispatched job (bob's agent, a child of the builder's job) wears it.
	child, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-try", RoomID: "room-1", MessageSeq: 9, RequesterUserID: "maya", OwnerUserID: "bob", Provider: "claude",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-bob", DispatchedByJobID: "job-builder", DispatchDepth: 1,
		ExpertID: d.ID, ExpertVersion: "1.0.0", ExpertName: "Drafted (draft)", CreatedAtMs: 2})
	if err != nil {
		t.Fatal(err)
	}
	effective := func(job string) wire.ContentEffective {
		t.Helper()
		code, raw := g.do("bob", http.MethodGet, "/v1/content-components/effective?job="+job+"&room=room-1", "", nil)
		var eff wire.ContentEffective
		if code != http.StatusOK || json.Unmarshal(raw, &eff) != nil {
			t.Fatalf("effective = %d %s", code, raw)
		}
		return eff
	}
	eff := effective(child.JobID)
	if eff.Expert == nil || eff.Expert.ComponentID != d.ID || eff.Expert.Owner != "jack" || eff.Expert.BundleKey != stored.BundleBlobKey {
		t.Fatalf("effective expert = %+v (missing %v)", eff.Expert, eff.Missing)
	}
	grant := "?job=" + child.JobID + "&room=room-1"
	if code, _ := g.do("bob", http.MethodGet, "/v1/artifacts/"+stored.BundleBlobKey+grant, "", nil); code != http.StatusOK {
		t.Fatalf("bundle under the job grant = %d", code)
	}
	if code, _ := g.do("maya", http.MethodGet, "/v1/artifacts/"+stored.BundleBlobKey, "", nil); code != http.StatusNotFound {
		t.Fatalf("bundle without a grant = %d", code)
	}

	// A job its parent did not stage it for does not wear it.
	stray, _ := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-stray", RoomID: "room-1", MessageSeq: 11, RequesterUserID: "maya", OwnerUserID: "bob", Provider: "claude",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-bob", ExpertID: d.ID, ExpertVersion: "1.0.0", ExpertName: "Drafted (draft)", CreatedAtMs: 3})
	if eff := effective(stray.JobID); eff.Expert != nil || len(eff.Missing) == 0 {
		t.Fatalf("stray job wears the draft: %+v", eff)
	}
	if code, _ := g.do("bob", http.MethodGet, "/v1/artifacts/"+stored.BundleBlobKey+"?job="+stray.JobID+"&room=room-1", "", nil); code != http.StatusNotFound {
		t.Fatalf("bundle for a stray job = %d", code)
	}

	// Expired: the try's job no longer wears it.
	stored.ExpiresAtMs = now - 1
	if err := g.svc.ContentComponents.PutContentDraft(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if eff := effective(child.JobID); eff.Expert != nil || len(eff.Missing) != 1 || !strings.Contains(eff.Missing[0], "Drafted (draft)") {
		t.Fatalf("expired draft = %+v", eff)
	}
}
