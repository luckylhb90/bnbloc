package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

// contentGateway: members maya (owner of room-1), jack (member of room-1),
// bob (member of room-1, not a lessee) and vic (a viewer); realtime's blob
// store on disk; memory content and job stores.
type contentGateway struct {
	t      *testing.T
	svc    *Service
	srv    *httptest.Server
	jobs   *store.MemoryBridgeJobs
	tokens map[string]string
}

func newContentGateway(t *testing.T) *contentGateway {
	t.Helper()
	svc := newGateway(t, "")
	// The content suites run many writes per member; the edge budget is
	// tested on its own (ratelimit_test.go).
	svc.limiter.get(1000, 1000, nil)
	svc.Verifier = subjectVerifier{}
	svc.TenantResolver = func(id auth.Identity, _ string) (string, string, error) {
		if id.Subject == "vic" {
			return "home", string(rbac.RoleViewer), nil
		}
		return "home", string(rbac.RoleMember), nil
	}
	members := map[string]string{"maya": "owner", "jack": "member", "bob": "member", "vic": "member"}
	svc.Rooms = func(string, string) []string { return nil }
	svc.Membership = roleMembership(func(subject, room string) (string, bool) {
		if room != "room-1" {
			return "", false
		}
		return members[subject], members[subject] != ""
	})
	svc.RoomTenant = func(string) string { return "home" }
	blobs, err := blobstore.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rtSrv := httptest.NewServer(svcauth.Middleware(svcauth.KeyFromEnv(), (&realtimehttp.Service{Blobs: blobs}).Handler()))
	t.Cleanup(rtSrv.Close)
	svc.Backends.Realtime = rtSrv.URL
	svc.PlatformSettings = store.NewMemoryPlatformSettings()
	svc.ContentComponents = store.NewMemoryContentComponents()
	jobs := store.NewMemoryBridgeJobs()
	svc.ContentJobs = jobs
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	g := &contentGateway{t: t, svc: svc, srv: srv, jobs: jobs, tokens: map[string]string{}}
	for u := range members {
		g.tokens[u], _ = login(t, srv, u)
	}
	return g
}

func (g *contentGateway) do(user, method, path, contentType string, body []byte) (int, []byte) {
	g.t.Helper()
	req, _ := http.NewRequest(method, g.srv.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+g.tokens[user])
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (g *contentGateway) json(user, method, path string, body any) (int, []byte) {
	g.t.Helper()
	raw, _ := json.Marshal(body)
	return g.do(user, method, path, "application/json", raw)
}

func expertForm(instructions string) map[string]any {
	return map[string]any{"form": map[string]any{
		"kind": "expert", "name": "reviewer", "displayName": "Reviewer", "instructions": instructions,
		"quickPrompts": []map[string]string{{"title": "Review", "prompt": "Review the diff"}},
		"skills":       []map[string]string{{"name": "checklist", "description": "Use when reviewing", "body": "1. tests"}},
	}}
}

func decodeDetail(t *testing.T, raw []byte) wire.ContentComponentDetail {
	t.Helper()
	var d wire.ContentComponentDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return d
}

// A member saves an expert by form, sees it in the Library and in
// /v1/components, reads a version and downloads its bundle; nobody else
// sees or downloads it; a duplicate name and a viewer's save are refused.
func TestContentCreateListReadDownload(t *testing.T) {
	g := newContentGateway(t)
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components", expertForm("Be strict. MARKER"))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	d := decodeDetail(t, raw)
	c := d.Component
	if c.Kind != "expert" || c.Name != "reviewer" || c.Version != "1.0.0" || !c.Mine || !c.Acked || c.Scope != "personal" || len(c.Skills) != 1 || len(c.QuickPrompts) != 1 {
		t.Fatalf("component = %+v", c)
	}
	if len(d.Versions) != 1 || d.Versions[0].BundleURL != "/v1/artifacts/"+d.Versions[0].BundleKey {
		t.Fatalf("versions = %+v", d.Versions)
	}
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components", expertForm("again")); code != http.StatusConflict || !strings.Contains(string(raw), "already have") {
		t.Fatalf("duplicate = %d %s", code, raw)
	}
	if code, _ := g.json("vic", http.MethodPost, "/v1/content-components", expertForm("x")); code != http.StatusForbidden {
		t.Fatalf("viewer create = %d", code)
	}
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components", map[string]any{"form": map[string]any{"kind": "expert", "name": "Bad Name"}}); code != http.StatusBadRequest {
		t.Fatalf("invalid = %d %s", code, raw)
	}

	_, raw = g.do("maya", http.MethodGet, "/v1/content-components?kind=expert", "", nil)
	var list struct{ Components []wire.ContentComponent }
	_ = json.Unmarshal(raw, &list)
	if len(list.Components) != 1 || list.Components[0].ID != c.ID {
		t.Fatalf("maya's list = %s", raw)
	}
	_, raw = g.do("jack", http.MethodGet, "/v1/content-components", "", nil)
	_ = json.Unmarshal(raw, &list)
	if len(list.Components) != 0 {
		t.Fatalf("jack's list = %s", raw)
	}
	_, raw = g.do("maya", http.MethodGet, "/v1/components", "", nil)
	var catalog struct {
		Components        []wire.MemberComponent
		ContentComponents []wire.ContentComponent
	}
	_ = json.Unmarshal(raw, &catalog)
	if len(catalog.ContentComponents) != 1 || catalog.ContentComponents[0].ID != c.ID {
		t.Fatalf("catalog = %s", raw)
	}

	code, raw = g.do("maya", http.MethodGet, "/v1/content-components/"+c.ID+"/versions/1.0.0", "", nil)
	var vd wire.ContentVersionDetail
	_ = json.Unmarshal(raw, &vd)
	if code != http.StatusOK || strings.TrimSpace(vd.Instructions) != "Be strict. MARKER" || vd.SkillBodies["checklist"] == "" || len(vd.Files) != 3 {
		t.Fatalf("version = %d %s", code, raw)
	}
	if code, _ := g.do("jack", http.MethodGet, "/v1/content-components/"+c.ID, "", nil); code != http.StatusNotFound {
		t.Fatalf("jack get = %d", code)
	}
	if code, body := g.do("maya", http.MethodGet, d.Versions[0].BundleURL, "", nil); code != http.StatusOK || len(body) == 0 {
		t.Fatalf("maya download = %d", code)
	}
	if code, _ := g.do("jack", http.MethodGet, d.Versions[0].BundleURL, "", nil); code != http.StatusNotFound {
		t.Fatalf("jack download = %d", code)
	}
}

// A new version is immutable and newer: a form edit on a base keeps the
// base's extra files and bumps the patch; an older version is refused; only
// the owner saves versions; delete removes it everywhere.
func TestContentVersionsAndDelete(t *testing.T) {
	g := newContentGateway(t)
	_, raw := g.json("maya", http.MethodPost, "/v1/content-components", expertForm("v1"))
	c := decodeDetail(t, raw).Component
	req := expertForm("v2")
	req["baseVersion"] = "1.0.0"
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+c.ID+"/versions", req)
	d := decodeDetail(t, raw)
	if code != http.StatusCreated || d.Component.LatestVersion != "1.0.1" || len(d.Versions) != 2 || d.Versions[0].Version != "1.0.1" {
		t.Fatalf("new version = %d %s", code, raw)
	}
	req["version"] = "1.0.0"
	if code, _ := g.json("maya", http.MethodPost, "/v1/content-components/"+c.ID+"/versions", req); code != http.StatusConflict {
		t.Fatalf("older version = %d", code)
	}
	if code, _ := g.json("jack", http.MethodPost, "/v1/content-components/"+c.ID+"/versions", expertForm("x")); code != http.StatusNotFound {
		t.Fatalf("other's version = %d", code)
	}
	if code, _ := g.do("jack", http.MethodDelete, "/v1/content-components/"+c.ID, "", nil); code != http.StatusNotFound {
		t.Fatalf("other's delete = %d", code)
	}
	if code, _ := g.do("maya", http.MethodDelete, "/v1/content-components/"+c.ID, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := g.do("maya", http.MethodGet, "/v1/content-components/"+c.ID, "", nil); code != http.StatusNotFound {
		t.Fatalf("after delete = %d", code)
	}
	if code, _ := g.do("maya", http.MethodGet, d.Versions[0].BundleURL, "", nil); code != http.StatusNotFound {
		t.Fatalf("download after delete = %d", code)
	}
}

func zipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// A skill directory imports as a skill pack; a later import with ?id= is a
// new version of the same component.
func TestContentImport(t *testing.T) {
	g := newContentGateway(t)
	skill := "---\nname: lint-rules\ndescription: Use when linting\n---\nRun the linter.\n"
	code, raw := g.do("maya", http.MethodPost, "/v1/content-components/import?kind=skill", "application/zip", zipArchive(t, map[string]string{"lint-rules/SKILL.md": skill, "lint-rules/scripts/run.sh": "echo"}))
	if code != http.StatusCreated {
		t.Fatalf("import = %d %s", code, raw)
	}
	c := decodeDetail(t, raw).Component
	if c.Kind != "skill" || c.Name != "lint-rules" || !c.NeedsSandbox || len(c.Permissions) != 1 || c.Permissions[0] != "scripts" {
		t.Fatalf("imported = %+v", c)
	}
	code, raw = g.do("maya", http.MethodPost, "/v1/content-components/import?id="+c.ID, "application/zip", zipArchive(t, map[string]string{"SKILL.md": skill}))
	if code != http.StatusCreated || decodeDetail(t, raw).Component.LatestVersion != "1.0.1" {
		t.Fatalf("import version = %d %s", code, raw)
	}
	if code, _ := g.do("maya", http.MethodPost, "/v1/content-components/import?kind=bogus", "application/zip", zipArchive(t, map[string]string{"SKILL.md": skill})); code != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d", code)
	}
}

// Room settings: the room owner pins an expert and enables a skill pack;
// a plain member cannot; members read them (and the bundles) through the
// room; a member enables their own skill pack for all their agents but not
// someone else's.
func TestContentUses(t *testing.T) {
	g := newContentGateway(t)
	_, raw := g.json("maya", http.MethodPost, "/v1/content-components", expertForm("pinned"))
	ex := decodeDetail(t, raw)
	_, raw = g.json("maya", http.MethodPost, "/v1/content-components", map[string]any{"form": map[string]any{"kind": "skill", "name": "kit", "skills": []map[string]string{{"name": "a", "description": "does a", "body": "a"}}}})
	kit := decodeDetail(t, raw).Component

	if code, raw := g.json("maya", http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: "room", RoomID: "room-1", ComponentID: ex.Component.ID, Version: "1.0.0"}); code != http.StatusNoContent {
		t.Fatalf("pin expert = %d %s", code, raw)
	}
	if code, _ := g.json("maya", http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: "room", RoomID: "room-1", ComponentID: kit.ID}); code != http.StatusNoContent {
		t.Fatalf("room pack = %d", code)
	}
	if code, _ := g.json("jack", http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: "room", RoomID: "room-1", ComponentID: kit.ID}); code != http.StatusForbidden {
		t.Fatalf("member room edit = %d", code)
	}
	if code, _ := g.json("jack", http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: "member", ComponentID: kit.ID}); code != http.StatusNotFound {
		t.Fatalf("jack enables maya's pack = %d", code)
	}
	if code, _ := g.json("maya", http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: "member", ComponentID: ex.Component.ID}); code != http.StatusBadRequest {
		t.Fatalf("member expert = %d", code)
	}
	if code, _ := g.json("maya", http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: "member", ComponentID: kit.ID}); code != http.StatusNoContent {
		t.Fatalf("member pack = %d", code)
	}

	code, raw := g.do("jack", http.MethodGet, "/v1/content-uses?room=room-1", "", nil)
	var uses wire.ContentUsesReply
	_ = json.Unmarshal(raw, &uses)
	if code != http.StatusOK || len(uses.Room) != 2 || len(uses.Member) != 0 || uses.Room[0].Component == nil || uses.Room[0].Component.Version != "1.0.0" || uses.Room[0].Component.Mine {
		t.Fatalf("jack's uses = %d %s", code, raw)
	}
	if code, _ := g.do("jack", http.MethodGet, "/v1/content-components/"+ex.Component.ID+"?room=room-1", "", nil); code != http.StatusOK {
		t.Fatalf("jack reads pinned = %d", code)
	}
	if code, _ := g.do("jack", http.MethodGet, ex.Versions[0].BundleURL+"?room=room-1", "", nil); code != http.StatusOK {
		t.Fatalf("jack downloads pinned = %d", code)
	}
	if code, _ := g.do("jack", http.MethodPost, "/v1/content-components/"+ex.Component.ID+"/versions/1.0.0/ack?room=room-1", "", nil); code != http.StatusNoContent {
		t.Fatalf("ack = %d", code)
	}
	_, raw = g.do("jack", http.MethodGet, "/v1/content-uses?room=room-1", "", nil)
	_ = json.Unmarshal(raw, &uses)
	if !uses.Room[0].Component.Acked {
		t.Fatalf("after ack = %s", raw)
	}
	if code, _ := g.do("maya", http.MethodDelete, "/v1/content-uses?scope=room&roomId=room-1&componentId="+kit.ID, "", nil); code != http.StatusNoContent {
		t.Fatalf("remove room pack = %d", code)
	}
}

// The bridge running a job resolves its content and downloads the bundles
// under the job's grant; nobody else can, and not after the job ended.
func TestContentEffectiveForJob(t *testing.T) {
	g := newContentGateway(t)
	_, raw := g.json("maya", http.MethodPost, "/v1/content-components", expertForm("job expert"))
	ex := decodeDetail(t, raw)
	ctx := tenant.NewContext(context.Background(), "home")
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	job, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", MessageSeq: 3, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", ExpertID: ex.Component.ID, ExpertVersion: "1.0.0", ExpertName: "Reviewer", CreatedAtMs: 1})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/content-components/effective?job=" + job.JobID + "&room=room-1"
	code, raw := g.do("jack", http.MethodGet, path, "", nil)
	var eff wire.ContentEffective
	_ = json.Unmarshal(raw, &eff)
	if code != http.StatusOK || eff.Expert == nil || eff.Expert.ComponentID != ex.Component.ID || eff.Expert.Owner != "maya" || eff.Expert.BundleKey != ex.Versions[0].BundleKey {
		t.Fatalf("effective = %d %s", code, raw)
	}
	grant := "?job=" + job.JobID + "&room=room-1"
	if code, _ := g.do("jack", http.MethodGet, ex.Versions[0].BundleURL+grant, "", nil); code != http.StatusOK {
		t.Fatalf("lessee download = %d", code)
	}
	if code, _ := g.do("bob", http.MethodGet, path, "", nil); code != http.StatusForbidden {
		t.Fatalf("other bridge = %d", code)
	}
	if code, _ := g.do("bob", http.MethodGet, ex.Versions[0].BundleURL+grant, "", nil); code != http.StatusNotFound {
		t.Fatalf("other download = %d", code)
	}
	if code, _ := g.do("jack", http.MethodGet, "/v1/content-components/effective?job=nope&room=room-1", "", nil); code != http.StatusNotFound {
		t.Fatalf("missing job = %d", code)
	}
	if _, err := g.jobs.UpdateAgentJobStatus(ctx, store.JobStatusUpdate{JobID: job.JobID, BridgeID: "br-jack", Status: store.AgentJobReplied, NowMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if code, _ := g.do("jack", http.MethodGet, path, "", nil); code != http.StatusConflict {
		t.Fatalf("ended job = %d", code)
	}
	if code, _ := g.do("jack", http.MethodGet, ex.Versions[0].BundleURL+grant, "", nil); code != http.StatusNotFound {
		t.Fatalf("ended download = %d", code)
	}
}
