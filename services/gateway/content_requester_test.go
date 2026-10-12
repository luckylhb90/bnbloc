package gateway

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// packageTar packs files as a content bundle.
func packageTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	in := map[string][]byte{}
	for k, v := range files {
		in[k] = []byte(v)
	}
	raw, err := tarbundle.PackFiles(in, tarbundle.ContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func draftExpert(name string, extra map[string]string) map[string]string {
	files := map[string]string{
		"component.json":  `{"kind":"bnbloc.expert.package/v1","name":"` + name + `","version":"1.0.0","displayName":"Drafted"}`,
		"INSTRUCTIONS.md": "Be thorough.\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

// A bridge saves a drafted package on behalf of the job it runs: as the
// job's requester (their Library, personal scope), only while the job runs
// on that bridge, only while the requester is a member, with the
// validator's message on a bad package; a borrowed agent saves no scripts.
func TestContentSaveAsJobRequester(t *testing.T) {
	g := newContentGateway(t)
	audit := store.NewMemoryExtensionAudit()
	g.svc.ExtAudit = audit
	ctx := tenant.NewContext(context.Background(), "home")
	now := time.Now().UnixMilli()
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatal(err)
	}
	seq := int64(0)
	put := func(id, requester string) {
		t.Helper()
		seq++
		if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: id, RoomID: "room-1", MessageSeq: seq, RequesterUserID: requester, OwnerUserID: "jack", Provider: "opencode",
			Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	put("job-borrowed", "maya") // maya asked jack's agent
	put("job-own", "jack")      // jack asked his own agent
	put("job-gone", "kim")      // kim is not a member of the room
	grant := func(job string) string { return "?job=" + job + "&room=room-1" }
	tarCT := "application/x-tar"

	// Saved as the requester: maya owns it, jack (the bridge) does not.
	code, raw := g.do("jack", http.MethodPost, "/v1/content-components"+grant("job-borrowed"), tarCT, packageTar(t, draftExpert("drafted", nil)))
	saved := decodeDetail(t, raw).Component
	if code != http.StatusCreated || saved.Owner != "maya" || saved.Scope != "personal" || !saved.Mine || saved.LatestVersion != "1.0.0" {
		t.Fatalf("save as requester = %d %s", code, raw)
	}
	if _, raw := g.do("maya", http.MethodGet, "/v1/content-components?kind=expert", "", nil); !bytes.Contains(raw, []byte(saved.ID)) {
		t.Fatalf("maya's library misses it: %s", raw)
	}
	if code, _ := g.do("jack", http.MethodGet, "/v1/content-components/"+saved.ID, "", nil); code != http.StatusNotFound {
		t.Fatalf("jack reads maya's component without a grant = %d", code)
	}
	// get_component / list_components read as the requester.
	if code, raw := g.do("jack", http.MethodGet, "/v1/content-components/"+saved.ID+grant("job-borrowed"), "", nil); code != http.StatusOK || !decodeDetail(t, raw).Component.Mine {
		t.Fatalf("read as requester = %d %s", code, raw)
	}
	if code, raw := g.do("jack", http.MethodGet, "/v1/content-components"+grant("job-borrowed")+"&kind=expert", "", nil); code != http.StatusOK || !bytes.Contains(raw, []byte(saved.ID)) {
		t.Fatalf("list as requester = %d %s", code, raw)
	}

	// A new version of the requester's component (version bumped).
	code, raw = g.do("jack", http.MethodPost, "/v1/content-components/"+saved.ID+"/versions"+grant("job-borrowed"), tarCT, packageTar(t, draftExpert("drafted", map[string]string{"INSTRUCTIONS.md": "Be stricter.\n"})))
	if code != http.StatusCreated || decodeDetail(t, raw).Component.LatestVersion != "1.0.1" {
		t.Fatalf("new version as requester = %d %s", code, raw)
	}
	if code, _ := g.do("jack", http.MethodPost, "/v1/content-components/"+saved.ID+"/versions", tarCT, packageTar(t, draftExpert("drafted", nil))); code != http.StatusNotFound {
		t.Fatalf("new version without a grant = %d", code)
	}
	// Creating the same name again is a conflict that says what to do.
	if code, raw := g.do("jack", http.MethodPost, "/v1/content-components"+grant("job-borrowed"), tarCT, packageTar(t, draftExpert("drafted", nil))); code != http.StatusConflict || !strings.Contains(string(raw), "new version") {
		t.Fatalf("duplicate = %d %s", code, raw)
	}

	// The validator's message comes back.
	bad := map[string]string{"component.json": `{"kind":"bnbloc.expert.package/v1","name":"bad","version":"1.0.0","displayName":"Bad"}`}
	if code, raw := g.do("jack", http.MethodPost, "/v1/content-components"+grant("job-borrowed"), tarCT, packageTar(t, bad)); code != http.StatusBadRequest || !strings.Contains(string(raw), "INSTRUCTIONS.md") {
		t.Fatalf("invalid package = %d %s", code, raw)
	}

	// Scripts: refused from someone else's agent, accepted from one's own.
	scripted := draftExpert("scripted", map[string]string{"skills/run/SKILL.md": "---\nname: run\ndescription: Use to run it\n---\nRun it.\n", "skills/run/scripts/go.sh": "echo hi\n"})
	scripted["component.json"] = `{"kind":"bnbloc.expert.package/v1","name":"scripted","version":"1.0.0","displayName":"Scripted","requires":{"permissions":["scripts"]}}`
	if code, raw := g.do("jack", http.MethodPost, "/v1/content-components"+grant("job-borrowed"), tarCT, packageTar(t, scripted)); code != http.StatusForbidden || !strings.Contains(string(raw), "own agent") {
		t.Fatalf("borrowed scripts = %d %s", code, raw)
	}
	if code, raw := g.do("jack", http.MethodPost, "/v1/content-components"+grant("job-own"), tarCT, packageTar(t, scripted)); code != http.StatusCreated || decodeDetail(t, raw).Component.Owner != "jack" {
		t.Fatalf("own scripts = %d %s", code, raw)
	}

	// Only the bridge running the job, only a member requester.
	if code, _ := g.do("bob", http.MethodPost, "/v1/content-components"+grant("job-borrowed"), tarCT, packageTar(t, draftExpert("other", nil))); code != http.StatusForbidden {
		t.Fatalf("someone else's bridge = %d", code)
	}
	if code, _ := g.do("maya", http.MethodPost, "/v1/content-components"+grant("job-borrowed"), tarCT, packageTar(t, draftExpert("other", nil))); code != http.StatusForbidden {
		t.Fatalf("the requester herself with a job grant (not her bridge) = %d", code)
	}
	if code, _ := g.do("jack", http.MethodPost, "/v1/content-components"+grant("job-gone"), tarCT, packageTar(t, draftExpert("other", nil))); code != http.StatusForbidden {
		t.Fatalf("non-member requester = %d", code)
	}
	if code, _ := g.do("jack", http.MethodPost, "/v1/content-components?job=nope&room=room-1", tarCT, packageTar(t, draftExpert("other", nil))); code != http.StatusNotFound {
		t.Fatalf("missing job = %d", code)
	}
	if _, err := g.jobs.UpdateAgentJobStatus(ctx, store.JobStatusUpdate{JobID: "job-borrowed", BridgeID: "br-jack", Status: store.AgentJobReplied, NowMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if code, _ := g.do("jack", http.MethodPost, "/v1/content-components"+grant("job-borrowed"), tarCT, packageTar(t, draftExpert("late", nil))); code != http.StatusConflict {
		t.Fatalf("ended job = %d", code)
	}

	// Each save is audited with the agent, the requester and the bridge.
	recs, err := audit.AuditLog(context.Background(), store.AuditLogQuery{AllTenants: true, Kinds: []string{wire.AuditKindContent}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	saves := 0
	for _, r := range recs {
		if r.Action == "save" {
			saves++
			if r.Name == "drafted@1.0.0" && (r.Actor != "maya" || !strings.Contains(r.Detail, "agent jack/opencode") || !strings.Contains(r.Detail, "bridge of jack")) {
				t.Fatalf("audit = %+v", r)
			}
		}
	}
	if saves != 3 {
		t.Fatalf("audited saves = %d, want 3 (create, new version, own scripted)", saves)
	}
}
