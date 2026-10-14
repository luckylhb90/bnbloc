package roomtools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/services/room"
)

// roomServiceGateway is the bridge's gateway in front of the real room
// service: every artifact read is the room service's own (membership, the
// artifact's room), as the bridge owner. Run records answer from runs.
type roomServiceGateway struct {
	t     *testing.T
	room  *httptest.Server
	owner string
	runs  map[string]string
}

func (g *roomServiceGateway) Do(_ context.Context, method, path string, body any) ([]byte, error) {
	if run, ok := g.runs[path]; ok {
		return []byte(run), nil
	}
	return g.call(g.owner, method, path, body)
}

func (g *roomServiceGateway) call(user, method, path string, body any) ([]byte, error) {
	g.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, g.room.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{Status: resp.StatusCode, Body: raw}
	}
	return raw, nil
}

// artifactRooms is a room service with three rooms: room-1 (maya and jack),
// room-2 (maya and jack) and room-3 (maya only). maya's agent published a
// deliverable in each. jack's bridge reads.
func artifactRooms(t *testing.T, bigContent string) *roomServiceGateway {
	t.Helper()
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	mem := store.NewMemoryRoom()
	members := map[string][]string{"room-1": {"maya", "jack"}, "room-2": {"maya", "jack"}, "room-3": {"maya"}}
	for id, users := range members {
		if err := mem.CreateRoom(ctx, store.Room{RoomID: id, Name: id, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		for _, u := range users {
			role := store.RoleMember
			if u == "maya" {
				role = store.RoleOwner
			}
			if err := mem.AddMember(ctx, store.Membership{RoomID: id, UserID: u, Role: role, JoinedAtMs: 1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	svc := room.New(mem, mem.Channels())
	svc.Artifacts = store.NewMemoryArtifacts()
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	g := &roomServiceGateway{t: t, room: srv, owner: "jack", runs: map[string]string{
		"/v1/rooms/room-1/jobs/job-a": `{"jobId":"job-a","agent":{"owner":"maya","core":"acme","provider":"opencode"}}`,
	}}
	publish := func(roomID, id, content string) {
		t.Helper()
		if _, err := g.call("maya", http.MethodPost, "/v1/rooms/"+roomID+"/artifacts", map[string]any{"artifactId": id, "kind": "code", "title": "drafts/x/" + id + ".json"}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if _, err := g.call("maya", http.MethodPost, "/v1/rooms/"+roomID+"/artifacts/"+id+"/versions", map[string]any{"baseVersion": 0, "content": content, "sourceJobId": "job-a", "sourceSeq": 3}); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	publish("room-1", "artf_component", `{"name":"weekly-report-assistant"}`)
	publish("room-1", "artf_big", bigContent)
	publish("room-2", "artf_elsewhere", "room-2 secret")
	publish("room-3", "artf_private", "room-3 secret")
	return g
}

func artifactEnv(gw Gateway, roomID string, bound bool) *Env {
	sess := &Session{scope: Scope{RoomID: roomID, Owner: "jack", Core: "nimbus", Provider: "codex"}}
	if bound {
		sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya", ChannelID: "ch-1", MessageSeq: 4})
	}
	return &Env{Session: sess, Gateway: gw}
}

// read_artifact returns a deliverable of the session's room, its producer
// and its text fenced as untrusted.
func TestReadArtifactReadsAnotherAgentsDeliverable(t *testing.T) {
	g := artifactRooms(t, "x")
	res := readArtifact(context.Background(), artifactEnv(g, "room-1", true), json.RawMessage(`{"artifactId":"artf_component"}`))
	if res.Err != "" {
		t.Fatalf("read = %s", res.Err)
	}
	out := res.Value.(map[string]any)
	if out["producedBy"] != "maya/opencode on core acme" || out["version"] != 1 {
		t.Fatalf("result = %+v", out)
	}
	content, _ := out["content"].(string)
	open, rest, _ := strings.Cut(content, "\n")
	if !strings.HasPrefix(open, "<<<untrusted-content ") || !strings.Contains(rest, `{"name":"weekly-report-assistant"}`) {
		t.Fatalf("content is not fenced:\n%s", content)
	}
	tag := strings.Fields(open)[1]
	tag = strings.TrimSuffix(tag, ":")
	if !strings.HasSuffix(content, "<<<end untrusted-content "+tag+">>>") {
		t.Fatalf("fence does not close with its nonce %q:\n%s", tag, content)
	}
	if res := readArtifact(context.Background(), artifactEnv(g, "room-1", false), json.RawMessage(`{"artifactId":"artf_component"}`)); !strings.Contains(res.Err, "only while you are answering") {
		t.Fatalf("unbound read = %+v", res)
	}
}

// A deliverable of another room is refused, even one the bridge owner may
// read in that room.
func TestReadArtifactRefusesAnotherRoomsDeliverable(t *testing.T) {
	g := artifactRooms(t, "x")
	res := readArtifact(context.Background(), artifactEnv(g, "room-1", true), json.RawMessage(`{"artifactId":"artf_elsewhere"}`))
	if res.Err == "" || strings.Contains(res.Err, "room-2 secret") {
		t.Fatalf("another room's deliverable = %+v", res)
	}
}

// A room the bridge owner is not a member of reads nothing.
func TestReadArtifactRefusesNonMember(t *testing.T) {
	g := artifactRooms(t, "x")
	res := readArtifact(context.Background(), artifactEnv(g, "room-3", true), json.RawMessage(`{"artifactId":"artf_private"}`))
	if !strings.Contains(res.Err, "not a member") {
		t.Fatalf("non-member read = %+v", res)
	}
}

// The text is capped.
func TestReadArtifactCapsItsSize(t *testing.T) {
	g := artifactRooms(t, strings.Repeat("abcdefgh", (maxArtifactText/8)*3))
	res := readArtifact(context.Background(), artifactEnv(g, "room-1", true), json.RawMessage(`{"artifactId":"artf_big"}`))
	if res.Err != "" {
		t.Fatalf("read = %s", res.Err)
	}
	out := res.Value.(map[string]any)
	content, _ := out["content"].(string)
	if out["truncated"] != true || len(content) > maxArtifactText+512 {
		t.Fatalf("read %d bytes of content (truncated=%v), want at most %d", len(content), out["truncated"], maxArtifactText)
	}
}

// The room tools never open a site ticket or a site URL: a site may be
// served from the sites origin, which never sees a session, so an agent
// reads a site's files only through the main origin's site file read
// (…/versions/{v}/files/{path}; services/gateway site_file_read_test.go
// reads one with a sites origin set).
func TestRoomToolsNeverOpenSiteTickets(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"site-ticket", "/v1/sites/", "siteUrl", "/site/"}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range forbidden {
			if bytes.Contains(raw, []byte(s)) {
				t.Errorf("%s names %q: room tools read a site through …/versions/{v}/files/{path}, never a site ticket or URL", f, s)
			}
		}
	}
}

// pathGateway answers fixed paths and 404 for every other one.
type pathGateway map[string]string

func (g pathGateway) Do(_ context.Context, _, path string, _ any) ([]byte, error) {
	if body, ok := g[path]; ok {
		return []byte(body), nil
	}
	return nil, &StatusError{Status: http.StatusNotFound}
}

// read_artifact reads a room attachment by its id: text fenced as data a
// person uploaded, binary and oversized ones described, not shown.
func TestReadArtifactReadsAnAttachment(t *testing.T) {
	const base = "/v1/rooms/room-1/attachments/"
	g := pathGateway{
		base + "att_notes/meta": `{"id":"att_notes","filename":"notes.md","contentType":"text/markdown","size":14}`,
		base + "att_notes":      "# plan\nstep 1\n",
		base + "att_img/meta":   `{"id":"att_img","filename":"shot.png","contentType":"image/png","size":4}`,
		base + "att_img":        "\x89PNG",
		base + "att_huge/meta":  `{"id":"att_huge","filename":"dump.log","contentType":"text/plain","size":104857600}`,
	}
	read := func(args string) Result {
		return readArtifact(context.Background(), artifactEnv(g, "room-1", true), json.RawMessage(args))
	}
	res := read(`{"artifactId":"att_notes"}`)
	out, _ := res.Value.(map[string]any)
	content, _ := out["content"].(string)
	if res.Err != "" || out["filename"] != "notes.md" || !strings.HasPrefix(content, "<<<untrusted-content ") || !strings.Contains(content, "step 1") {
		t.Fatalf("text attachment = %+v", res)
	}
	if out, _ := read(`{"artifactId":"att_img"}`).Value.(map[string]any); out["note"] != "binary content, not shown" || out["content"] != nil {
		t.Fatalf("binary attachment = %+v", out)
	}
	if out, _ := read(`{"artifactId":"att_huge"}`).Value.(map[string]any); !strings.Contains(out["note"].(string), "larger than") {
		t.Fatalf("huge attachment = %+v", out)
	}
	if res := read(`{"artifactId":"att_gone"}`); res.Err == "" {
		t.Fatalf("missing attachment = %+v", res)
	}
	if res := read(`{"artifactId":"att_notes","version":2}`); !strings.Contains(res.Err, "only to a deliverable") {
		t.Fatalf("attachment with a version = %+v", res)
	}
}
