package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// realSite is a site artifact in room whose version 1 is a real snapshot
// of files in the blob store.
func (e *appGW) realSite(room string, files map[string]string) string {
	e.t.Helper()
	m := map[string][]byte{}
	for k, v := range files {
		m[k] = []byte(v)
	}
	raw, err := tarbundle.PackFiles(m, tarbundle.SiteLimits)
	if err != nil {
		e.t.Fatal(err)
	}
	key, size, sha, err := e.blobs.Put(context.Background(), bytes.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	// The room service detects the app SDK while it stores a version
	// (putVersionBlob); this version is stored directly.
	var scan appdata.SDKScan
	_, _ = scan.Write(raw)
	id := e.artifact(room, store.ArtifactKindSite)
	if _, _, err := e.b.Artifacts.PublishArtifactVersion(e.ctx(), id, store.ArtifactVersion{Storage: store.ArtifactStorageBlob, BlobKey: key,
		ContentHash: sha, MimeType: tarbundle.SiteMIME, SizeBytes: size, AuthorUserID: "maya", CreatedAtMs: 1, LoadsAppSDK: scan.Found()}, 0); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// newRoom is a new room of members (the first its owner).
func (e *appGW) newRoom(members ...string) string {
	e.t.Helper()
	id := ids.New("room-")
	if err := e.b.Room.CreateRoomWithOwner(e.ctx(), store.Room{RoomID: id, Name: "r", CreatedBy: members[0], CreatedAtMs: 1},
		store.Membership{RoomID: id, UserID: members[0], Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
	for _, u := range members[1:] {
		if err := e.b.Room.AddMember(e.ctx(), store.Membership{RoomID: id, UserID: u, Role: store.RoleMember, JoinedAtMs: 1}); err != nil {
			e.t.Fatal(err)
		}
	}
	return id
}

// runJob puts a running job of maya's in room, leased by jack's bridge.
func (e *appGW) runJob(jobID, room string) {
	e.t.Helper()
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: jobID, RoomID: room, MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
}

// readArtifactAs runs the read_artifact room tool on jack's bridge, in
// room, bound to job.
func (e *appGW) readArtifactAs(room, job string, args map[string]any) roomtools.Result {
	e.t.Helper()
	return e.roomToolAs(room, job, "read_artifact", args)
}

// roomToolAs runs a room tool on jack's bridge, in room, bound to job (of
// maya's).
func (e *appGW) roomToolAs(room, job, name string, args map[string]any) roomtools.Result {
	e.t.Helper()
	gw := bridgeGateway{e: e, bearer: e.session("jack")}
	srv, err := roomtools.Start(roomtools.Config{Gateway: gw})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = srv.Close() })
	_, sess, err := srv.Open(roomtools.Scope{RoomID: room, TenantID: e.tenant, Owner: "jack", Provider: "codex"})
	if err != nil {
		e.t.Fatal(err)
	}
	sess.Bind(roomtools.Job{JobID: job, RequesterUserID: "maya"})
	tool, ok := roomtools.DefaultRegistry().Lookup(name)
	if !ok {
		e.t.Fatalf("no room tool %s", name)
	}
	raw, _ := json.Marshal(args)
	return tool.Call(context.Background(), &roomtools.Env{Session: sess, Gateway: gw}, raw)
}

// With a sites origin, read_artifact still reads a site's files: through
// the main origin's site file read with the bridge's session and the job's
// grant, never a site ticket. The file comes raw (no rewrite) and fenced.
func TestReadArtifactReadsSiteFileWithSitesOrigin(t *testing.T) {
	e := newAppGW(t)
	e.withSites()
	e.runJob("job-1", e.roomID)
	site := e.realSite(e.roomID, map[string]string{
		"index.html":   `<html><script src="/app.js"></script>poll</html>`,
		"js/app.js":    "console.log('lake')",
		"docs/a_b.txt": "nested",
	})
	for _, c := range []struct{ path, want string }{
		{"", `<script src="/app.js"></script>poll`},
		{"js/app.js", "console.log('lake')"},
		{"docs/a_b.txt", "nested"},
	} {
		res := e.readArtifactAs(e.roomID, "job-1", map[string]any{"artifactId": site, "path": c.path})
		if res.Err != "" {
			t.Fatalf("read_artifact %q = %s", c.path, res.Err)
		}
		out := res.Value.(map[string]any)
		content, _ := out["content"].(string)
		if !strings.Contains(content, c.want) || !strings.HasPrefix(content, "<<<untrusted-content ") {
			t.Fatalf("read_artifact %q = %v", c.path, out)
		}
	}
	if res := e.readArtifactAs(e.roomID, "job-1", map[string]any{"artifactId": site, "path": "missing.js"}); !strings.Contains(res.Err, "no file missing.js") {
		t.Fatalf("a missing file = %+v", res)
	}
	// The route answers the bytes as a sandboxed attachment, on the main
	// origin only.
	jack := "Authorization: Bearer " + e.session("jack")
	path := "/v1/rooms/" + e.roomID + "/artifacts/" + site + "/versions/1/files/index.html?job=job-1"
	r := e.call(http.MethodGet, path, nil, jack, mainHost)
	if r.status != http.StatusOK || r.header.Get("Content-Security-Policy") != "sandbox" || !strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment") ||
		r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("site file read = %d %v %s", r.status, r.header, r.body)
	}
	if r := e.call(http.MethodGet, path, nil, jack, sitesHost); r.status != http.StatusNotFound {
		t.Fatalf("site file read on the sites origin = %d, want 404", r.status)
	}
}

// Who reads: the caller must reach the room, a job grant must be the
// caller's live job, and the room service decides as the job's requester.
func TestSiteFileReadRefusesNonMembers(t *testing.T) {
	e := newAppGW(t)
	e.runJob("job-1", e.roomID)
	site := e.realSite(e.roomID, map[string]string{"index.html": "secret"})
	path := "/v1/rooms/" + e.roomID + "/artifacts/" + site + "/versions/1/files/index.html"
	// maya, a member, reads; bob, not one, does not.
	if r := e.call(http.MethodGet, path, nil, "Authorization: Bearer "+e.session("maya")); r.status != http.StatusOK || string(r.body) != "secret" {
		t.Fatalf("a member's read = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, path+"?job=job-1", nil, "Authorization: Bearer "+e.session("bob")); r.status != http.StatusForbidden || strings.Contains(string(r.body), "secret") {
		t.Fatalf("a non-member's read = %d %s", r.status, r.body)
	}
	// A bridge member running no such job is refused its grant.
	if r := e.call(http.MethodGet, path+"?job=job-9", nil, "Authorization: Bearer "+e.session("jack")); r.status == http.StatusOK {
		t.Fatalf("a read under another job = %d %s", r.status, r.body)
	}
	// The tool in a room jack's bridge is not in reads nothing.
	private := e.newRoom("maya")
	e.runJob("job-p", private)
	privSite := e.realSite(private, map[string]string{"index.html": "private"})
	if res := e.readArtifactAs(private, "job-p", map[string]any{"artifactId": privSite}); res.Err == "" || strings.Contains(res.Err, "private") {
		t.Fatalf("read_artifact outside the bridge's rooms = %+v", res)
	}
	if r := e.call(http.MethodGet, "/v1/rooms/"+private+"/artifacts/"+privSite+"/versions/1/files/index.html?job=job-p", nil, "Authorization: Bearer "+e.session("jack")); r.status != http.StatusForbidden {
		t.Fatalf("the route outside the bridge's rooms = %d %s", r.status, r.body)
	}
	// The job's requester left the room: the grant reads nothing.
	if err := e.b.Room.RemoveMember(e.ctx(), e.roomID, "maya"); err != nil {
		t.Fatal(err)
	}
	if r := e.call(http.MethodGet, path+"?job=job-1", nil, "Authorization: Bearer "+e.session("jack")); r.status != http.StatusForbidden {
		t.Fatalf("a read for a requester who left = %d %s", r.status, r.body)
	}
}

// A site of another room is not read through this room's path, by the
// route or the tool, though the bridge's owner may read it in its own.
func TestSiteFileReadRefusesAnotherRoomsSite(t *testing.T) {
	e := newAppGW(t)
	e.runJob("job-1", e.roomID)
	other := e.newRoom("maya", "jack")
	site := e.realSite(other, map[string]string{"index.html": "elsewhere"})
	jack := "Authorization: Bearer " + e.session("jack")
	if r := e.call(http.MethodGet, "/v1/rooms/"+other+"/artifacts/"+site+"/versions/1/files/index.html", nil, jack); r.status != http.StatusOK {
		t.Fatalf("the site in its own room = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/artifacts/"+site+"/versions/1/files/index.html?job=job-1", nil, jack); r.status != http.StatusNotFound || strings.Contains(string(r.body), "elsewhere") {
		t.Fatalf("another room's site = %d %s", r.status, r.body)
	}
	if res := e.readArtifactAs(e.roomID, "job-1", map[string]any{"artifactId": site}); res.Err == "" || strings.Contains(res.Err, "elsewhere") {
		t.Fatalf("read_artifact of another room's site = %+v", res)
	}
}

// One file over the cap is refused (413); the rest of the site reads.
func TestSiteFileReadCapsItsSize(t *testing.T) {
	e := newAppGW(t)
	e.runJob("job-1", e.roomID)
	site := e.realSite(e.roomID, map[string]string{
		"index.html": "small",
		"big.txt":    strings.Repeat("x", siteFileReadMaxBytes+1),
	})
	jack := "Authorization: Bearer " + e.session("jack")
	base := "/v1/rooms/" + e.roomID + "/artifacts/" + site + "/versions/1/files/"
	if r := e.call(http.MethodGet, base+"big.txt?job=job-1", nil, jack); r.status != http.StatusRequestEntityTooLarge || len(r.body) > 1024 {
		t.Fatalf("an oversized file = %d (%d bytes)", r.status, len(r.body))
	}
	if r := e.call(http.MethodGet, base+"index.html?job=job-1", nil, jack); r.status != http.StatusOK || string(r.body) != "small" {
		t.Fatalf("a small file = %d %s", r.status, r.body)
	}
	if res := e.readArtifactAs(e.roomID, "job-1", map[string]any{"artifactId": site, "path": "big.txt"}); !strings.Contains(res.Err, "more than a read answers") {
		t.Fatalf("read_artifact of an oversized file = %+v", res)
	}
}
