package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/gateway/appsdk"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
	roomsvc "github.com/luckylhb90/bnbloc/services/room"
)

const appGWKey = "app-gateway-internal-key"

// appGW is a gateway in front of a real room service (memory store, app
// data on) and a real realtime hub, as composed in production: the room
// service publishes app_changed through realtime's signed ingest. One room
// ("maya" owner, "jack" member) holds two site apps, A and B, and a
// markdown artifact. The organization ("home", named "Home Org") has maya,
// jack and nora (not in the room); olaf belongs to another one ("away").
type appGW struct {
	t       *testing.T
	b       *store.Backends
	room    *roomsvc.Service
	roomURL string
	hub     *realtime.Hub
	gw      *Service
	srv     *httptest.Server
	jobs    *store.MemoryBridgeJobs
	blobs   *blobstore.DiskStore
	tenant  string
	roomID  string
	appA    string
	appB    string
	doc     string
	// appCalls counts the app data calls the backends received: the room
	// service's /internal/v1/app/… and realtime's app change feed.
	appCalls atomic.Int64
}

// countAppCalls counts h's app data requests in e.appCalls.
func (e *appGW) countAppCalls(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/v1/app/") || strings.HasSuffix(r.URL.Path, "/app-events") {
			e.appCalls.Add(1)
		}
		h.ServeHTTP(w, r)
	})
}

func newAppGW(t *testing.T) *appGW {
	t.Helper()
	t.Setenv("BNBLOC_INTERNAL_KEY", appGWKey)
	b, err := store.NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	e := &appGW{t: t, b: b, tenant: "home"}
	e.hub = realtime.NewHub(64, 64)
	disk, err := blobstore.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.blobs = disk
	rt := &realtimehttp.Service{Hub: e.hub, Blobs: disk, Attachments: b.Attachments}
	rtSrv := httptest.NewServer(e.countAppCalls(svcauth.Middleware(appGWKey, rt.Handler())))
	t.Cleanup(rtSrv.Close)

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	box, err := sealbox.New(raw)
	if err != nil {
		t.Fatal(err)
	}
	e.room = roomsvc.New(b.Room, b.Channel)
	e.room.Artifacts, e.room.Profiles, e.room.Tenants = b.Artifacts, b.Profiles, b.Tenant
	e.room.AppDocs, e.room.AppCipher, e.room.ShareBox = b.AppDocs, appdata.NewCipher(box), box
	e.room.RoomNotes = b.RoomNotes
	e.room.Publisher = realtime.NewHTTPPublisher(rtSrv.URL, "room")
	e.room.Blobs = realtime.NewHandoffClient(rtSrv.URL, "room")
	roomSrv := httptest.NewServer(e.countAppCalls(svcauth.Middleware(appGWKey, e.room.Handler())))
	t.Cleanup(roomSrv.Close)
	e.roomURL = roomSrv.URL
	// realtime's room routes (attachments) ask the room service.
	rt.Membership = membership.NewRoomClient(roomSrv.URL)

	e.gw = newGateway(t, roomSrv.URL)
	e.gw.Backends.Realtime = rtSrv.URL
	e.gw.Rooms = func(string, string) []string { return nil }
	e.gw.RoomTenant = func(string) string { return e.tenant }
	e.gw.Membership = roleMembership(func(subject, room string) (string, bool) {
		m, ok, err := b.Room.GetMember(e.ctx(), room, subject)
		if err != nil || !ok {
			return "", false
		}
		return string(m.Role), !m.AgentHost
	})
	e.gw.TenantMemberRole = func(tenantID, userID string) (string, bool) {
		m, ok, err := b.Tenant.GetMember(context.Background(), tenantID, userID)
		if err != nil || !ok {
			return "", false
		}
		return string(m.Role), true
	}
	for id, name := range map[string]string{e.tenant: "Home Org", "away": "Away Org"} {
		if err := b.Tenant.PutTenant(context.Background(), store.Tenant{TenantID: id, Name: name, Status: store.TenantActive, CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for user, tid := range map[string]string{"maya": e.tenant, "jack": e.tenant, "nora": e.tenant, "olaf": "away"} {
		if err := b.Tenant.PutMember(context.Background(), store.TenantMember{TenantID: tid, UserID: user, Role: store.OrgRoleMember}); err != nil {
			t.Fatal(err)
		}
	}
	e.jobs = store.NewMemoryBridgeJobs()
	e.gw.ContentJobs = e.jobs
	e.srv = httptest.NewServer(e.gw.Handler())
	t.Cleanup(e.srv.Close)

	e.roomID = ids.New("room-")
	if err := b.Room.CreateRoomWithOwner(e.ctx(), store.Room{RoomID: e.roomID, Name: "r", CreatedBy: "maya", CreatedAtMs: 1},
		store.Membership{RoomID: e.roomID, UserID: "maya", Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	if err := b.Room.AddMember(e.ctx(), store.Membership{RoomID: e.roomID, UserID: "jack", Role: store.RoleMember, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	e.appA, e.appB = e.site(e.roomID), e.site(e.roomID)
	e.doc = e.artifact(e.roomID, store.ArtifactKindMarkdown)
	return e
}

func (e *appGW) ctx() context.Context { return tenant.NewContext(context.Background(), e.tenant) }

func (e *appGW) artifact(room, kind string) string {
	e.t.Helper()
	a, err := e.b.Artifacts.PutArtifact(e.ctx(), store.Artifact{ArtifactID: ids.New("art_"), RoomID: room, Kind: kind, Title: "app", CreatedBy: "maya"})
	if err != nil {
		e.t.Fatal(err)
	}
	return a.ArtifactID
}

// site is a site artifact with one published snapshot version.
func (e *appGW) site(room string) string {
	e.t.Helper()
	id := e.artifact(room, store.ArtifactKindSite)
	sha := digest.Of([]byte(id))
	if _, _, err := e.b.Artifacts.PublishArtifactVersion(e.ctx(), id, store.ArtifactVersion{Storage: store.ArtifactStorageBlob, BlobKey: digest.Key(sha),
		ContentHash: sha, MimeType: tarbundle.SiteMIME, SizeBytes: 10, AuthorUserID: "maya", CreatedAtMs: 1}, 0); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *appGW) session(subject string) string {
	return e.gw.Signer.Issue(auth.Claims{Subject: subject, Tenant: e.tenant, Role: "member", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
}

// resp is one gateway answer.
type resp struct {
	status int
	body   []byte
	header http.Header
}

func (r resp) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.body, &m)
	return m
}

func (r resp) docs() []map[string]any {
	var p wire.AppDocPage
	_ = json.Unmarshal(r.body, &p)
	return p.Docs
}

// call makes one request to the gateway; headers are "Name: value" ("Host:
// h" sets the request's host).
func (e *appGW) call(method, path string, body any, headers ...string) resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ": ")
		if name == "Host" {
			req.Host = value
			continue
		}
		req.Header.Set(name, value)
	}
	r, err := noRedirectClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	out, _ := io.ReadAll(r.Body)
	return resp{status: r.StatusCode, body: out, header: r.Header}
}

// ticketBase is subject's member site base for app ("/v1/sites/<ticket>/").
func (e *appGW) ticketBase(subject, app string) string {
	e.t.Helper()
	r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+app+"/versions/1/site-ticket", nil, "Authorization: Bearer "+e.session(subject))
	base, _ := r.json()["siteUrl"].(string)
	if r.status != http.StatusOK || !strings.HasPrefix(base, "/v1/sites/") {
		e.t.Fatalf("site ticket for %s = %d %s", subject, r.status, r.body)
	}
	return base
}

// roomCall makes a member's signed call to the room service directly.
func (e *appGW) roomCall(subject, method, path string, body any) resp {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	req, err := svcauth.NewRequest(context.Background(), method, e.roomURL+path, bytes.NewReader(raw), subject, e.tenant)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	out, _ := io.ReadAll(r.Body)
	return resp{status: r.StatusCode, body: out}
}

// share mints a link anyone can open to artifact (following its latest
// version): its token and id.
func (e *appGW) share(artifact string) (tok, id string) {
	e.t.Helper()
	return e.shareWith(artifact, wire.ShareAccessAnyone)
}

// shareWith mints a link to artifact with access ("" = the default).
func (e *appGW) shareWith(artifact, access string) (tok, id string) {
	e.t.Helper()
	r := e.roomCall("maya", http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+artifact+"/shares", wire.ShareCreateRequest{Access: access})
	var out struct {
		Token string         `json:"token"`
		Share wire.ShareView `json:"share"`
	}
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &out) != nil || out.Token == "" {
		e.t.Fatalf("create share = %d %s", r.status, r.body)
	}
	return out.Token, out.Share.ShareID
}

func shareBase(tok string) string { return "/v1/public/shares/" + tok + "/site/" }

// visitor mints a visitor token for a share link (renewing prev).
func (e *appGW) visitor(tok, prev string) string {
	e.t.Helper()
	r := e.call(http.MethodPost, "/v1/public/shares/"+tok+"/visitor", wire.AppVisitorRenew{Token: prev})
	var out wire.AppVisitorToken
	if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil || out.Token == "" || out.ExpiresAtMs <= time.Now().UnixMilli() {
		e.t.Fatalf("visitor = %d %s", r.status, r.body)
	}
	return out.Token
}

func visitorHeader(tok string) string { return wire.AppVisitorHeader + ": " + tok }

// me is the caller's app id at base (headers as call's).
func (e *appGW) me(base string, headers ...string) string {
	e.t.Helper()
	r := e.call(http.MethodGet, base+"~app/me", nil, headers...)
	id, _ := r.json()["id"].(string)
	if r.status != http.StatusOK || id == "" {
		e.t.Fatalf("me at %s = %d %s", base, r.status, r.body)
	}
	return id
}

func idsOf(docs []map[string]any) []string {
	out := []string{}
	for _, d := range docs {
		out = append(out, d["_id"].(string))
	}
	return out
}

// End to end, gateway → room service → memory store: a member through
// their site ticket, a visitor through a public link, and an agent through
// its job, each in the app their capability names, with the platform's two
// rules applied by the room service.
func TestAppDataEndToEnd(t *testing.T) {
	e := newAppGW(t)
	jack := e.ticketBase("jack", e.appA)

	// A member: create-only per person, $inc, private by default.
	jackID := e.me(jack)
	if r := e.call(http.MethodPut, jack+"~app/c/votes/"+jackID, map[string]any{"doc": map[string]any{"choice": "B"}}, "If-None-Match: *"); r.status != http.StatusCreated {
		t.Fatalf("put ifAbsent = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPut, jack+"~app/c/votes/"+jackID, map[string]any{"doc": map[string]any{"choice": "C"}}, "If-None-Match: *"); r.status != http.StatusConflict {
		t.Fatalf("second put ifAbsent = %d %s", r.status, r.body)
	}
	r := e.call(http.MethodPost, jack+"~app/c/tally", map[string]any{"doc": map[string]any{"n": 1}, "public": true})
	if r.status != http.StatusCreated {
		t.Fatalf("insert = %d %s", r.status, r.body)
	}
	tally := r.json()["_id"].(string)
	if r := e.call(http.MethodPatch, jack+"~app/c/tally/"+tally, map[string]any{"$inc": map[string]any{"n": 2}}, "If-Match: 1"); r.status != http.StatusOK || r.json()["n"] != float64(3) {
		t.Fatalf("patch = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPatch, jack+"~app/c/tally/"+tally, map[string]any{"$inc": map[string]any{"n": 1}}, "If-Match: 1"); r.status != http.StatusConflict {
		t.Fatalf("stale ifVersion = %d %s", r.status, r.body)
	}

	// A visitor through a public link: a pseudonym, the public documents
	// only, and their own writes.
	tok, _ := e.share(e.appA)
	vis := visitorHeader(e.visitor(tok, ""))
	pub := shareBase(tok)
	visID := e.me(pub, vis)
	if visID == jackID {
		t.Fatal("a visitor shares a member's pseudonym")
	}
	if r := e.call(http.MethodGet, pub+"~app/c/votes", nil, vis); r.status != http.StatusOK || len(r.docs()) != 0 {
		t.Fatalf("a visitor reads a private vote: %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, pub+"~app/c/tally/"+tally, nil, vis); r.status != http.StatusOK || r.json()["_author"] != nil {
		t.Fatalf("a visitor reads the public tally = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodDelete, pub+"~app/c/tally/"+tally, nil, vis); r.status != http.StatusForbidden {
		t.Fatalf("a visitor deletes a member's document = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPut, pub+"~app/c/votes/"+visID, map[string]any{"doc": map[string]any{"choice": "A"}}, vis, "If-None-Match: *"); r.status != http.StatusCreated {
		t.Fatalf("a visitor's vote = %d %s", r.status, r.body)
	}
	// The member owns the app: every vote, with its real author.
	r = e.call(http.MethodGet, jack+"~app/c/votes", nil)
	if r.status != http.StatusOK || len(r.docs()) != 2 {
		t.Fatalf("an owner's find = %d %s", r.status, r.body)
	}

	// An agent through the job it runs: an owner, acting for the job's
	// requester; a bridge that does not run the job is refused.
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: "job-1", RoomID: e.roomID, MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	agentPath := "/v1/rooms/" + e.roomID + "/apps/" + e.appA
	bridge := "Authorization: Bearer " + e.session("jack")
	r = e.call(http.MethodGet, agentPath+"/c/votes?job=job-1", nil, bridge)
	if r.status != http.StatusOK || len(r.docs()) != 2 || r.docs()[0]["_author"] == nil {
		t.Fatalf("the agent's find = %d %s", r.status, r.body)
	}
	if id := e.call(http.MethodGet, agentPath+"/me?job=job-1", nil, bridge).json()["id"]; id == jackID || id == nil {
		t.Fatalf("the agent's id %v is its bridge owner's", id)
	}
	if r := e.call(http.MethodGet, agentPath+"/c/votes?job=job-1", nil, "Authorization: Bearer "+e.session("maya")); r.status != http.StatusForbidden {
		t.Fatalf("another member's bridge with the job = %d %s", r.status, r.body)
	}
	r = e.call(http.MethodGet, agentPath+"/stats?job=job-1", nil, bridge)
	if r.status != http.StatusOK || r.json()["documents"] != float64(3) {
		t.Fatalf("stats = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodDelete, agentPath+"/data?job=job-1", nil, bridge); r.status != http.StatusOK {
		t.Fatalf("wipe = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, jack+"~app/c/votes", nil); r.status != http.StatusOK || len(r.docs()) != 0 {
		t.Fatalf("after the wipe = %d %s", r.status, r.body)
	}
}

// A page's file goes through the gateway to the room service and the blob
// store, beyond a document's body bound: a visitor uploads through a public
// link, reads it back, and the owners read it with its real uploader; the
// frame's calls are answered cross-origin.
func TestAppFilesEndToEnd(t *testing.T) {
	e := newAppGW(t)
	tok, _ := e.share(e.appA)
	vis := visitorHeader(e.visitor(tok, ""))
	pub := shareBase(tok)
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00"), make([]byte, 200<<10)...)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="photo.png"`)
	h.Set("Content-Type", "image/png")
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(png)
	_ = mw.Close()
	r := e.call(http.MethodPost, pub+"~app/files", body.String(), vis, "Content-Type: "+mw.FormDataContentType(), "Origin: null")
	if r.status != http.StatusCreated {
		t.Fatalf("upload = %d %s", r.status, r.body)
	}
	if r.header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("an upload from the frame is not answered cross-origin: %v", r.header)
	}
	var f wire.AppFile
	_ = json.Unmarshal(r.body, &f)
	if f.Size != int64(len(png)) || f.Name != "photo.png" {
		t.Fatalf("file = %+v", f)
	}
	if r := e.call(http.MethodGet, pub+"~app/files/"+f.ID+"/content", nil, vis); r.status != http.StatusOK || !bytes.Equal(r.body, png) ||
		r.header.Get("Content-Type") != "image/png" || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("content = %d %v", r.status, r.header)
	}
	jack := e.ticketBase("jack", e.appA)
	var owned wire.AppFile
	if r := e.call(http.MethodGet, jack+"~app/files/"+f.ID, nil); r.status != http.StatusOK || json.Unmarshal(r.body, &owned) != nil || !strings.HasPrefix(owned.Author, "visitor:") {
		t.Fatalf("an owner reads %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, e.ticketBase("jack", e.appB)+"~app/files/"+f.ID, nil); r.status != http.StatusNotFound {
		t.Fatalf("another app reads the file: %d", r.status)
	}
	// Over the upload bound: a 413 the page can show, not a proxy failure.
	huge := strings.Repeat("x", int(wire.MaxAppUploadBytes)+1)
	if r := e.call(http.MethodPost, jack+"~app/files", huge, "Content-Type: multipart/form-data; boundary=b"); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("an upload over the bound = %d %s", r.status, r.body)
	}
	// A document keeps its own, smaller bound.
	if r := e.call(http.MethodPost, jack+"~app/c/big", strings.Repeat("x", int(wire.MaxAppRequestBytes)+1)); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a document over its bound = %d %s", r.status, r.body)
	}
}

// The scope comes from the capability alone: a ticket for app A never
// reaches app B, and an app, room or tenant named in the query or body is
// ignored. The room entry takes its room and app from the path, under the
// session's reach of that room.
func TestAppScopeFromCapabilityOnly(t *testing.T) {
	e := newAppGW(t)
	a, b := e.ticketBase("jack", e.appA), e.ticketBase("jack", e.appB)
	if r := e.call(http.MethodPost, b+"~app/c/notes", map[string]any{"doc": map[string]any{"in": "B"}}); r.status != http.StatusCreated {
		t.Fatalf("insert in B = %d %s", r.status, r.body)
	}
	smuggle := "?app=" + e.appB + "&appId=" + e.appB + "&artifactId=" + e.appB + "&room=other&roomId=other&tenantId=other"
	body := map[string]any{"doc": map[string]any{"in": "A"}, "appId": e.appB, "roomId": "other", "tenantId": "other"}
	if r := e.call(http.MethodPost, a+"~app/c/notes"+smuggle, body); r.status != http.StatusCreated {
		t.Fatalf("insert in A naming B = %d %s", r.status, r.body)
	}
	for _, c := range []struct {
		base, want string
	}{{a, "A"}, {b, "B"}} {
		r := e.call(http.MethodGet, c.base+"~app/c/notes"+smuggle, nil)
		docs := r.docs()
		if r.status != http.StatusOK || len(docs) != 1 || docs[0]["in"] != c.want {
			t.Fatalf("app %s's notes = %d %s", c.want, r.status, r.body)
		}
	}
	// The room entry: the path's app, never the query's.
	jack := "Authorization: Bearer " + e.session("jack")
	r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/apps/"+e.appA+"/c/notes"+smuggle, nil, jack)
	if docs := r.docs(); r.status != http.StatusOK || len(docs) != 1 || docs[0]["in"] != "A" {
		t.Fatalf("room entry for A = %d %s", r.status, r.body)
	}
	// Outside the caller's rooms, and an app of another room, are refused.
	other := ids.New("room-")
	if err := e.b.Room.CreateRoomWithOwner(e.ctx(), store.Room{RoomID: other, Name: "o", CreatedBy: "maya", CreatedAtMs: 1},
		store.Membership{RoomID: other, UserID: "maya", Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	foreign := e.site(other)
	if r := e.call(http.MethodGet, "/v1/rooms/"+other+"/apps/"+foreign+"/c/notes", nil, jack); r.status != http.StatusForbidden {
		t.Fatalf("a room the caller is not in = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/apps/"+foreign+"/c/notes", nil, jack); r.status != http.StatusNotFound {
		t.Fatalf("another room's app through this room = %d %s", r.status, r.body)
	}
	// A ticket is the capability: a forged one reaches nothing.
	if r := e.call(http.MethodGet, "/v1/sites/forged/~app/c/notes", nil); r.status != http.StatusNotFound {
		t.Fatalf("a forged ticket = %d %s", r.status, r.body)
	}
}

// TestAppCapabilityReadsNoScopeInput: apps.go takes an app request's scope
// only from its capability. It reads no path value but the capability's
// and the API's own, no query parameter but the job grant, no cookie and
// no form; the only body it decodes is the visitor route's token.
func TestAppCapabilityReadsNoScopeInput(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "apps.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	pathValues := map[string]bool{"ticket": true, "token": true, "roomId": true, "artifactId": true, "collection": true, "id": true}
	var problems []string
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			lit := func() string {
				if len(call.Args) != 1 {
					return ""
				}
				if l, ok := call.Args[0].(*ast.BasicLit); ok && l.Kind == token.STRING {
					v, _ := strconv.Unquote(l.Value)
					return v
				}
				return ""
			}
			switch name := sel.Sel.Name; {
			case name == "PathValue" && !pathValues[lit()]:
				problems = append(problems, fn.Name.Name+": PathValue("+lit()+")")
			case name == "Get" && isQueryCall(sel.X) && lit() != "job":
				problems = append(problems, fn.Name.Name+": Query().Get("+lit()+")")
			case name == "FormValue" || name == "PostFormValue" || name == "Cookie" || name == "Cookies":
				problems = append(problems, fn.Name.Name+": "+name)
			case strings.HasPrefix(name, "Decode") && fn.Name.Name != "handleAppVisitor":
				problems = append(problems, fn.Name.Name+": "+name)
			}
			return true
		})
	}
	if len(problems) > 0 {
		t.Fatalf("app requests read scope outside their capability:\n  %s", strings.Join(problems, "\n  "))
	}
}

func isQueryCall(x ast.Expr) bool {
	call, ok := x.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Query"
}

// A public link's app call needs a visitor token of that link, and a dead
// link (revoked, expired, or public links disabled for the organization)
// is refused at once, valid token or not.
func TestPublicAppNeedsVisitorAndLiveLink(t *testing.T) {
	e := newAppGW(t)
	tok, shareID := e.share(e.appA)
	pub := shareBase(tok)
	code := func(r resp) string { c, _ := r.json()["code"].(string); return c }
	if r := e.call(http.MethodGet, pub+"~app/c/votes", nil); r.status != http.StatusUnauthorized || code(r) != wire.AppVisitorRequired {
		t.Fatalf("no visitor token = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, pub+"~app/c/votes", nil, visitorHeader("garbage")); r.status != http.StatusUnauthorized || code(r) != wire.AppVisitorInvalid {
		t.Fatalf("a garbage visitor token = %d %s", r.status, r.body)
	}
	// A site ticket is not a visitor token.
	ticket := strings.TrimSuffix(strings.TrimPrefix(e.ticketBase("jack", e.appA), "/v1/sites/"), "/")
	if r := e.call(http.MethodGet, pub+"~app/c/votes", nil, visitorHeader(ticket)); r.status != http.StatusUnauthorized || code(r) != wire.AppVisitorInvalid {
		t.Fatalf("a site ticket as a visitor token = %d %s", r.status, r.body)
	}
	vis := visitorHeader(e.visitor(tok, ""))
	if r := e.call(http.MethodPost, pub+"~app/c/votes", map[string]any{"doc": map[string]any{"v": 1}}, vis); r.status != http.StatusCreated {
		t.Fatalf("a visitor's insert = %d %s", r.status, r.body)
	}
	dead := func(why string) {
		t.Helper()
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			if r := e.call(m, pub+"~app/c/votes", map[string]any{"doc": map[string]any{"v": 2}}, vis); r.status != http.StatusNotFound {
				t.Fatalf("%s: %s = %d %s", why, m, r.status, r.body)
			}
		}
		if r := e.call(http.MethodPost, "/v1/public/shares/"+tok+"/visitor", nil); r.status != http.StatusNotFound {
			t.Fatalf("%s: visitor mint = %d %s", why, r.status, r.body)
		}
	}
	// Expired (the room service's clock passes the link's expiry).
	now := e.room.Now
	e.room.Now = func() int64 { return now() + 365*24*3600_000 }
	dead("expired")
	e.room.Now = now
	// Anonymous links disabled for the organization.
	publicLinks := func(off bool) {
		t.Helper()
		if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{AnonymousLinksDisabled: &off}); err != nil {
			t.Fatal(err)
		}
	}
	publicLinks(true)
	dead("disabled")
	publicLinks(false)
	if r := e.call(http.MethodGet, pub+"~app/c/votes", nil, vis); r.status != http.StatusOK {
		t.Fatalf("live again = %d %s", r.status, r.body)
	}
	// Revoked.
	if r := e.roomCall("maya", http.MethodDelete, "/v1/rooms/"+e.roomID+"/artifacts/"+e.appA+"/shares/"+shareID, nil); r.status != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", r.status, r.body)
	}
	dead("revoked")

	// A link to something that is not an app mints no visitor.
	docTok, _ := e.share(e.doc)
	if r := e.call(http.MethodPost, "/v1/public/shares/"+docTok+"/visitor", nil); r.status != http.StatusNotFound {
		t.Fatalf("a document link's visitor = %d %s", r.status, r.body)
	}
}

// A visitor token is bound to the link it was minted for: another link of
// the same app refuses it, and renewing it through its own link keeps the
// visitor while renewing it through another link makes a new one. Minting
// spends the client IP's visitor budget.
func TestVisitorTokensBoundToTheirLink(t *testing.T) {
	e := newAppGW(t)
	tok1, _ := e.share(e.appA)
	tok2, _ := e.share(e.appA)
	v1 := e.visitor(tok1, "")
	if r := e.call(http.MethodGet, shareBase(tok2)+"~app/me", nil, visitorHeader(v1)); r.status != http.StatusUnauthorized || r.json()["code"] != wire.AppVisitorInvalid {
		t.Fatalf("link 1's visitor on link 2 = %d %s", r.status, r.body)
	}
	id1 := e.me(shareBase(tok1), visitorHeader(v1))
	if renewed := e.visitor(tok1, v1); e.me(shareBase(tok1), visitorHeader(renewed)) != id1 {
		t.Fatal("a renewal through the same link changed the visitor")
	}
	v2 := e.visitor(tok2, v1)
	if e.me(shareBase(tok2), visitorHeader(v2)) == id1 {
		t.Fatal("another link's token renewed the same visitor")
	}
	// The visitor budget: a burst, then 429.
	spent := false
	for i := 0; i < int(defaultVisitorBurst)+5 && !spent; i++ {
		spent = e.call(http.MethodPost, "/v1/public/shares/"+tok1+"/visitor", nil).status == http.StatusTooManyRequests
	}
	if !spent {
		t.Fatal("visitor minting is not limited per IP")
	}
}

// The change feed carries only its app's app_changed events, metadata
// only: another app's changes and the room's other events never reach it.
// It ends once its capability dies.
func TestAppChangeFeedCarriesOnlyItsApp(t *testing.T) {
	e := newAppGW(t)
	e.gw.appFeedRecheck = 50 * time.Millisecond
	tok, shareID := e.share(e.appA)
	vis := visitorHeader(e.visitor(tok, ""))
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+shareBase(tok)+"~app/events", nil)
	req.Header.Set(wire.AppVisitorHeader, strings.TrimPrefix(vis, wire.AppVisitorHeader+": "))
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK || !strings.HasPrefix(stream.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("feed = %d %s", stream.StatusCode, stream.Header.Get("Content-Type"))
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stream.Body)
		for sc.Scan() {
			if l := sc.Text(); l != "" && !strings.HasPrefix(l, ":") {
				lines <- l
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for e.hub.SubscriberCount(e.roomID) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	jackB := e.ticketBase("jack", e.appB)
	jackA := e.ticketBase("jack", e.appA)
	if r := e.call(http.MethodPost, jackB+"~app/c/votes", map[string]any{"doc": map[string]any{"secret": "in-b"}}); r.status != http.StatusCreated {
		t.Fatalf("insert in B = %d %s", r.status, r.body)
	}
	e.hub.Publish(wire.Event{RoomID: e.roomID, Kind: wire.KindChatMessage, Payload: map[string]any{"text": "room chat"}})
	r := e.call(http.MethodPost, jackA+"~app/c/votes", map[string]any{"doc": map[string]any{"secret": "in-a"}})
	if r.status != http.StatusCreated {
		t.Fatalf("insert in A = %d %s", r.status, r.body)
	}
	next := func() string {
		t.Helper()
		select {
		case l, ok := <-lines:
			if !ok {
				return ""
			}
			return l
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return ""
	}
	if got := next(); got != "event: app_changed" {
		t.Fatalf("first line %q", got)
	}
	want := `data: {"appId":"` + e.appA + `","collection":"votes","id":"` + r.json()["_id"].(string) + `","version":1}`
	if got := next(); got != want {
		t.Fatalf("data %q, want %q", got, want)
	}
	// The link's revocation ends the feed at its next re-check.
	if r := e.roomCall("maya", http.MethodDelete, "/v1/rooms/"+e.roomID+"/artifacts/"+e.appA+"/shares/"+shareID, nil); r.status != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", r.status, r.body)
	}
	if got := next(); got != "" {
		t.Fatalf("the feed went on after the link died: %q", got)
	}
}

// appEventsOf writes only its app's app_changed events, re-encoded with
// the wire fields alone, and keep-alives; nothing else of the upstream
// stream passes.
func TestAppEventsOfNarrowsToTheApp(t *testing.T) {
	upstream := strings.Join([]string{
		": connected", "",
		"event: chat_message", `data: {"appId":"art_a","text":"chat"}`, "",
		"event: app_changed", `data: {"appId":"art_b","collection":"c","id":"x","version":1}`, "",
		"event: app_changed", `data: {"appId":"art_a","collection":"c","id":"d1","version":2,"body":"smuggled"}`, "",
		"event: app_changed", `data: not json`, "",
		`data: {"appId":"art_a","collection":"c","id":"no-event","version":3}`, "",
		": keep-alive", "",
	}, "\n")
	out, err := io.ReadAll(appEventsOf(io.NopCloser(strings.NewReader(upstream)), "art_a"))
	if err != nil {
		t.Fatal(err)
	}
	want := ": keep-alive\n\nevent: app_changed\ndata: {\"appId\":\"art_a\",\"collection\":\"c\",\"id\":\"d1\",\"version\":2}\n\n: keep-alive\n\n"
	if string(out) != want {
		t.Fatalf("narrowed stream:\n%s\nwant:\n%s", out, want)
	}
}

// The SDK and its types are served under both frame entries, versioned
// with the gateway, and the SDK's wire constants are the wire package's.
// The rest of ~app/ is reserved (never a site file), and an app frame's
// cross-origin calls get CORS without credentials.
func TestAppSDKServed(t *testing.T) {
	e := newAppGW(t)
	tok, _ := e.share(e.appA)
	for _, base := range []string{e.ticketBase("jack", e.appA), shareBase(tok)} {
		r := e.call(http.MethodGet, base+"~app/sdk.js", nil)
		if r.status != http.StatusOK || !bytes.Equal(r.body, appsdk.JS) || !strings.HasPrefix(r.header.Get("Content-Type"), "text/javascript") || r.header.Get("ETag") == "" {
			t.Fatalf("sdk.js at %s = %d %s", base, r.status, r.header)
		}
		if r := e.call(http.MethodGet, base+"~app/sdk.js", nil, "If-None-Match: "+r.header.Get("ETag")); r.status != http.StatusNotModified {
			t.Fatalf("sdk.js revalidation = %d", r.status)
		}
		if r := e.call(http.MethodGet, base+"~app/sdk.d.ts", nil); r.status != http.StatusOK || !bytes.Equal(r.body, appsdk.Types) {
			t.Fatalf("sdk.d.ts at %s = %d", base, r.status)
		}
		if r := e.call(http.MethodGet, base+"~app/nothing-here", nil); r.status != http.StatusNotFound {
			t.Fatalf("a reserved ~app path = %d %s", r.status, r.body)
		}
		r = e.call(http.MethodOptions, base+"~app/c/votes", nil, "Origin: null", "Access-Control-Request-Method: PUT", "Access-Control-Request-Headers: if-none-match, "+wire.AppVisitorHeader)
		if r.status != http.StatusNoContent || r.header.Get("Access-Control-Allow-Origin") != "*" ||
			!strings.Contains(r.header.Get("Access-Control-Allow-Headers"), wire.AppVisitorHeader) || r.header.Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("preflight at %s = %d %v", base, r.status, r.header)
		}
	}
	for name, want := range map[string]string{
		"VISITOR_HEADER":    strconv.Quote(wire.AppVisitorHeader),
		"VISITOR_REQUEST":   strconv.Quote(wire.AppVisitorRequest),
		"VISITOR_GRANT":     strconv.Quote(wire.AppVisitorGrant),
		"HANDSHAKE_VERSION": strconv.Itoa(wire.AppHandshakeVersion),
		"VISITOR_REQUIRED":  strconv.Quote(wire.AppVisitorRequired),
		"VISITOR_INVALID":   strconv.Quote(wire.AppVisitorInvalid),
		"CHANGE_EVENT":      strconv.Quote(wire.EventAppChanged),
		"MAX_DOC_BYTES":     strconv.Itoa(wire.MaxAppDocBytes),
		"MAX_FILE_BYTES":    strconv.Itoa(wire.MaxAttachmentBytes),
		// The demo mode's in-memory data space keeps the API's bounds.
		"MAX_FIND_LIMIT":          strconv.Itoa(wire.MaxAppFindLimit),
		"MAX_COLLECTION_NAME_LEN": strconv.Itoa(wire.MaxAppCollectionNameLen),
		"MAX_DOC_ID_LEN":          strconv.Itoa(wire.MaxAppDocIDLen),
	} {
		m := regexp.MustCompile(`var ` + name + ` = ([^;]+);`).FindSubmatch(appsdk.JS)
		if m == nil || string(m[1]) != want {
			t.Errorf("sdk.js %s = %q, want %s", name, m, want)
		}
	}
}

// Every app budget is stricter for writes than for reads, and a
// participant who spends their write budget is refused (429) while their
// reads go on.
func TestAppRatesWritesStricter(t *testing.T) {
	for d, r := range appRates {
		if !(r.write.rate < r.read.rate && r.write.burst <= r.read.burst) || r.write.rate <= 0 {
			t.Errorf("app budget %d: write %+v is not stricter than read %+v", d, r.write, r.read)
		}
	}
	e := newAppGW(t)
	jack := e.ticketBase("jack", e.appA)
	limited := false
	for i := 0; i < int(appRates[appByParticipant].write.burst)+5 && !limited; i++ {
		limited = e.call(http.MethodPost, jack+"~app/c/votes", map[string]any{"doc": map[string]any{"i": i}}).status == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("a participant's writes are not limited")
	}
	if r := e.call(http.MethodGet, jack+"~app/c/votes", nil); r.status != http.StatusOK {
		t.Fatalf("a read after the write budget = %d %s", r.status, r.body)
	}
}

// Capability tokens (a site ticket, a share token, a visitor token) never
// reach the gateway's request log.
func TestAppCapabilityTokensNotLogged(t *testing.T) {
	e := newAppGW(t)
	var buf bytes.Buffer
	logged := httptest.NewServer(obs.Middleware(slog.New(slog.NewJSONHandler(&buf, nil)), obs.NewMetrics("gateway-test"), e.gw.Handler()))
	defer logged.Close()
	e.srv = logged
	jack := e.ticketBase("jack", e.appA)
	tok, _ := e.share(e.appA)
	vis := e.visitor(tok, "")
	if r := e.call(http.MethodPost, jack+"~app/c/votes", map[string]any{"doc": map[string]any{"v": 1}}); r.status != http.StatusCreated {
		t.Fatalf("ticket insert = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, shareBase(tok)+"~app/c/votes", nil, visitorHeader(vis)); r.status != http.StatusOK {
		t.Fatalf("visitor find = %d %s", r.status, r.body)
	}
	_ = e.call(http.MethodGet, shareBase(tok)+"~app/sdk.js", nil)
	ticket := strings.TrimSuffix(strings.TrimPrefix(jack, "/v1/sites/"), "/")
	log := buf.String()
	if !strings.Contains(log, "~app") {
		t.Fatalf("the app calls were not logged: %s", log)
	}
	for name, secret := range map[string]string{"site ticket": ticket, "share token": tok, "visitor token": vis} {
		if strings.Contains(log, secret) {
			t.Errorf("the %s reached the request log", name)
		}
	}
}
