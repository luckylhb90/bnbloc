package room

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"reflect"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

// roomStoreMembership adapts the room store to realtime's membership port, so
// the two in-process services agree on who is in which room.
type roomStoreMembership struct{ rooms *store.MemoryRoom }

func (m roomStoreMembership) IsHumanMember(roomID, userID, tenantID string) bool {
	mem, ok := m.rooms.GetMember(tenant.NewContext(context.Background(), tenantID), roomID, userID)
	return ok && !mem.AgentHost
}

func (m roomStoreMembership) IsMember(roomID, userID, tenantID string) bool {
	return m.rooms.IsMember(tenant.NewContext(context.Background(), tenantID), roomID, userID)
}

// handoffEnv runs the real room and realtime services in-process over HTTP,
// wired exactly like production (room -> realtime via the handoff client).
type handoffEnv struct {
	t       *testing.T
	rooms   *store.MemoryRoom
	rt      *realtimehttp.Service
	rtSrv   *httptest.Server
	roomSrv *httptest.Server
	svc     *Service
}

func newHandoffEnv(t *testing.T) *handoffEnv {
	t.Helper()
	rooms := store.NewMemoryRoom()
	blobs, err := blobstore.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rt := &realtimehttp.Service{
		Hub:         realtime.NewHub(256, 64),
		Membership:  roomStoreMembership{rooms},
		Chat:        store.NewMemoryChat(),
		Channels:    rooms.Channels(),
		Attachments: store.NewMemoryAttachments(),
		Blobs:       blobs,
	}
	rtSrv := httptest.NewServer(rt.Handler())
	t.Cleanup(rtSrv.Close)
	svc := New(rooms, rooms.Channels())
	svc.Artifacts = store.NewMemoryArtifacts()
	svc.RoomImports = store.NewMemoryRoomImports()
	client := realtime.NewHandoffClient(rtSrv.URL)
	svc.Handoff = client
	svc.Blobs = client
	roomSrv := httptest.NewServer(svc.Handler())
	t.Cleanup(roomSrv.Close)
	return &handoffEnv{t: t, rooms: rooms, rt: rt, rtSrv: rtSrv, roomSrv: roomSrv, svc: svc}
}

func (e *handoffEnv) do(srv *httptest.Server, method, path, user string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("X-Bnbloc-User", user)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (e *handoffEnv) must(srv *httptest.Server, method, path, user string, body any) []byte {
	e.t.Helper()
	code, out := e.do(srv, method, path, user, body)
	if code != http.StatusOK {
		e.t.Fatalf("%s %s as %s: %d %s", method, path, user, code, out)
	}
	return out
}

func (e *handoffEnv) chat(roomID, user string, body map[string]any) int64 {
	e.t.Helper()
	var r struct {
		Sequence int64 `json:"sequence"`
	}
	_ = json.Unmarshal(e.must(e.rtSrv, http.MethodPost, "/v1/rooms/"+roomID+"/chat", user, body), &r)
	return r.Sequence
}

func (e *handoffEnv) upload(roomID, user, filename, contentType string, content []byte) string {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	h.Set("Content-Type", contentType)
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(content)
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, e.rtSrv.URL+"/v1/rooms/"+roomID+"/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var meta struct {
		ID string `json:"id"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&meta) != nil {
		e.t.Fatalf("upload: %d", resp.StatusCode)
	}
	return meta.ID
}

func (e *handoffEnv) history(roomID, user, channelID string) []wire.FeedMessage {
	e.t.Helper()
	q := "?limit=200"
	if channelID != "" {
		q += "&channelId=" + channelID
	}
	var page wire.FeedPage
	if err := json.Unmarshal(e.must(e.rtSrv, http.MethodGet, "/v1/rooms/"+roomID+"/history"+q, user, nil), &page); err != nil {
		e.t.Fatal(err)
	}
	return page.Messages
}

func (e *handoffEnv) channels(roomID, user string) []channelView {
	e.t.Helper()
	var out struct {
		Channels []channelView `json:"channels"`
	}
	_ = json.Unmarshal(e.must(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/channels", user, nil), &out)
	return out.Channels
}

func (e *handoffEnv) importPackage(user string, pkg []byte) (int, []byte) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "handoff.zip")
	_, _ = part.Write(pkg)
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, e.roomSrv.URL+"/v1/rooms/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// pngBytes is a minimal payload the attachment sniffer classifies as PNG.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)

// seedRoom builds a room with two channels, human chat (one with an
// attachment, one addressed to an agent, one edited, one soft-deleted), an
// agent reply batch, and an artifact with two versions.
func seedRoom(e *handoffEnv) (roomID string, deletedSeq int64) {
	roomID = "launch"
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms", "alice", map[string]any{"roomId": roomID, "name": "Launch plan", "description": "Q4 launch"})
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms/"+roomID+"/members", "alice", map[string]any{"userId": "bob", "role": "member"})
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms/"+roomID+"/members", "alice", map[string]any{"userId": "jack", "role": "admin"})
	var side channelView
	_ = json.Unmarshal(e.must(e.roomSrv, http.MethodPost, "/v1/rooms/"+roomID+"/channels", "bob", map[string]any{"name": "design"}), &side)

	att := e.upload(roomID, "alice", "mock.png", "image/png", pngBytes)
	e.chat(roomID, "alice", map[string]any{"text": "here is the mock", "attachmentIds": []string{att}, "clientMsgId": "c-1"})
	e.chat(roomID, "bob", map[string]any{"text": "@jack/codex summarize", "recipient": map[string]any{"ownerId": "jack", "provider": "codex", "core": "acme"}})
	edited := e.chat(roomID, "bob", map[string]any{"text": "typo"})
	e.must(e.rtSrv, http.MethodPatch, fmt.Sprintf("/v1/rooms/%s/messages/%d", roomID, edited), "bob", map[string]any{"text": "fixed"})
	deletedSeq = e.chat(roomID, "alice", map[string]any{"text": "oops, secret"})
	e.must(e.rtSrv, http.MethodDelete, fmt.Sprintf("/v1/rooms/%s/messages/%d", roomID, deletedSeq), "alice", nil)
	e.chat(roomID, "bob", map[string]any{"text": "design channel note", "channelId": side.ChannelID})
	// An agent reply batch, through the same internal ingest activity uses
	// (activity stamps the reply's channel on the event and in the payload).
	def := e.channels(roomID, "alice")[0].ChannelID
	e.must(e.rtSrv, http.MethodPost, "/internal/v1/rooms/"+roomID+"/publish", "service:activity", wire.Event{
		Kind: wire.KindAgentMessages, AtUnixMs: 1_700_000_000_000, ChannelID: def,
		Payload: map[string]any{
			"workspaceId": roomID, "agentSessionId": "sess-1", "channelId": def,
			"messages": []map[string]any{{
				"seq": 1, "createdAtUnixMs": 1_700_000_000_000,
				"payload": map[string]any{"role": "assistant", "text": "Summary: ship it", "ownerUserId": "jack", "provider": "codex"},
			}},
		},
	})
	var art struct {
		Artifact store.Artifact `json:"artifact"`
	}
	_ = json.Unmarshal(e.must(e.roomSrv, http.MethodPost, "/v1/rooms/"+roomID+"/artifacts", "alice",
		map[string]any{"kind": "markdown", "title": "Spec", "content": "# v1"}), &art)
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms/"+roomID+"/artifacts/"+art.Artifact.ArtifactID+"/versions", "bob",
		map[string]any{"baseVersion": 1, "content": "# v2", "summary": "second pass"})
	return roomID, deletedSeq
}

func readZip(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("export is not a zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		out[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	return out
}

func writeZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range files {
		fw, _ := zw.Create(name)
		_, _ = fw.Write(b)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// comparable reduces a feed row to what a handoff must preserve: kind, time,
// attribution and content — modulo sequence, ids, and the imported marker.
func comparable(t *testing.T, m wire.FeedMessage) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		t.Fatal(err)
	}
	delete(p, "imported")
	delete(p, "workspaceId")
	delete(p, "channelId")
	if atts, ok := p["attachments"].([]any); ok {
		for _, a := range atts {
			delete(a.(map[string]any), "id")
		}
	}
	text := ""
	if s, ok := p["text"].(string); ok {
		text = s
	}
	return map[string]any{"kind": m.Kind, "at": m.AtUnixMs, "payload": p, "text": text}
}

func TestHandoffExportImportRoundTrip(t *testing.T) {
	e := newHandoffEnv(t)
	roomID, deletedSeq := seedRoom(e)
	srcChannels := e.channels(roomID, "alice")

	pkg := e.must(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/export", "alice", nil)
	files := readZip(t, pkg)
	for _, name := range []string{"manifest.json", "messages.jsonl", "transcript.md"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("package lacks %s", name)
		}
	}
	var man wire.RoomExportManifest
	if err := json.Unmarshal(files["manifest.json"], &man); err != nil {
		t.Fatal(err)
	}
	if man.Format != wire.RoomExportFormat || man.Room.ID != roomID || man.Room.Name != "Launch plan" || man.ExportedBy != "alice" {
		t.Fatalf("manifest = %+v", man)
	}
	if man.MessageCount != 5 || man.AttachmentCount != 1 || man.ArtifactCount != 1 || len(man.Members) != 3 || len(man.Channels) != 2 {
		t.Fatalf("manifest counts = %+v", man)
	}
	if !reflect.DeepEqual(man.Agents, []wire.RoomExportAgent{{Owner: "jack", Provider: "codex", Core: "acme"}}) {
		t.Fatalf("agents = %+v", man.Agents)
	}
	if len(man.AgentSessions) != 1 || man.AgentSessions[0].AgentSessionID != "sess-1" || man.AgentSessions[0].Owner != "jack" || man.AgentSessions[0].MessageCount != 1 {
		t.Fatalf("agent sessions = %+v", man.AgentSessions)
	}
	// Soft-deleted messages never leave the room.
	if bytes.Contains(files["messages.jsonl"], []byte("oops, secret")) || bytes.Contains(files["transcript.md"], []byte("oops, secret")) {
		t.Fatal("soft-deleted message exported")
	}
	if bytes.Contains(files["messages.jsonl"], []byte(fmt.Sprintf(`"sequence":%d,`, deletedSeq))) {
		t.Fatal("soft-deleted sequence exported")
	}
	tr := string(files["transcript.md"])
	for _, want := range []string{"# Launch plan", "**jack/codex** (agent)", "Summary: ship it", "**alice**", "_Attachments: mock.png_", "→ @jack/codex"} {
		if !strings.Contains(tr, want) {
			t.Fatalf("transcript lacks %q:\n%s", want, tr)
		}
	}

	// Import as someone outside the source room: a new room they alone own.
	code, out := e.importPackage("dave", pkg)
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, out)
	}
	var res wire.RoomImportResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if res.RoomID == "" || res.RoomID == roomID || res.ImportedMessages != 5 || res.ImportedAttachments != 1 || res.ImportedArtifacts != 1 || len(res.SkippedAttachments) != 0 {
		t.Fatalf("import result = %+v", res)
	}
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	if members := e.rooms.Members(ctx, res.RoomID); len(members) != 1 || members[0].UserID != "dave" || members[0].Role != store.RoleOwner {
		t.Fatalf("imported room members = %+v (the file's member list must not grant access)", members)
	}
	if e.rooms.IsMember(ctx, res.RoomID, "alice") {
		t.Fatal("source member gained access from the package")
	}
	if len(res.Handoff.SuggestedMembers) != 3 || res.Handoff.SourceRoomID != roomID {
		t.Fatalf("handoff = %+v", res.Handoff)
	}
	var info wire.RoomHandoffInfo
	_ = json.Unmarshal(e.must(e.roomSrv, http.MethodGet, "/v1/rooms/"+res.RoomID+"/handoff", "dave", nil), &info)
	if info.SourceRoomName != "Launch plan" || info.ImportedBy != "dave" {
		t.Fatalf("provenance = %+v", info)
	}
	if code, _ := e.do(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/handoff", "alice", nil); code != http.StatusNotFound {
		t.Fatalf("non-imported room handoff = %d, want 404", code)
	}

	// History equals the source, channel by channel, modulo seq/ids.
	dstChannels := e.channels(res.RoomID, "dave")
	if len(dstChannels) != len(srcChannels) {
		t.Fatalf("channels = %+v, want %d", dstChannels, len(srcChannels))
	}
	for i := range srcChannels {
		if srcChannels[i].Name != dstChannels[i].Name || srcChannels[i].IsDefault != dstChannels[i].IsDefault {
			t.Fatalf("channel %d: %+v vs %+v", i, srcChannels[i], dstChannels[i])
		}
		src := e.history(roomID, "alice", srcChannels[i].ChannelID)
		dst := e.history(res.RoomID, "dave", dstChannels[i].ChannelID)
		if len(src) != len(dst) {
			t.Fatalf("channel %s: %d vs %d messages", srcChannels[i].Name, len(src), len(dst))
		}
		for j := range src {
			if a, b := comparable(t, src[j]), comparable(t, dst[j]); !reflect.DeepEqual(a, b) {
				t.Fatalf("message %d differs:\n src %v\n dst %v", j, a, b)
			}
			var p struct {
				Imported *wire.ImportedMarker `json:"imported"`
			}
			_ = json.Unmarshal(dst[j].Payload, &p)
			if p.Imported == nil || p.Imported.FromRoomID != roomID || p.Imported.FromRoomName != "Launch plan" || p.Imported.OriginalSequence != src[j].Sequence {
				t.Fatalf("message %d imported marker = %+v", j, p.Imported)
			}
			if (src[j].EditedAtMs != nil) != (p.Imported.EditedAtMs != nil) {
				t.Fatalf("message %d edit stamp not carried", j)
			}
		}
	}

	// The re-uploaded attachment is downloadable from the new room.
	for _, m := range e.history(res.RoomID, "dave", "") {
		var p feedPayload
		_ = json.Unmarshal(m.Payload, &p)
		for _, a := range p.Attachments {
			code, body := e.do(e.rtSrv, http.MethodGet, "/v1/rooms/"+res.RoomID+"/attachments/"+a.ID, "dave", nil)
			if code != http.StatusOK || !bytes.Equal(body, pngBytes) {
				t.Fatalf("imported attachment %s: %d", a.ID, code)
			}
		}
	}

	// Artifacts come back with their full version history.
	var arts struct {
		Artifacts []store.Artifact `json:"artifacts"`
	}
	_ = json.Unmarshal(e.must(e.roomSrv, http.MethodGet, "/v1/rooms/"+res.RoomID+"/artifacts", "dave", nil), &arts)
	if len(arts.Artifacts) != 1 || arts.Artifacts[0].CurrentVersion != 2 || arts.Artifacts[0].Title != "Spec" {
		t.Fatalf("imported artifacts = %+v", arts.Artifacts)
	}

	// Imported rows carry no author: nobody can edit or delete them as theirs.
	first := e.history(res.RoomID, "dave", "")[0]
	if code, _ := e.do(e.rtSrv, http.MethodDelete, fmt.Sprintf("/v1/rooms/%s/messages/%d", res.RoomID, first.Sequence), "dave", nil); code != http.StatusForbidden {
		t.Fatalf("delete imported row = %d, want 403", code)
	}

	// Nothing was published live: the hub's replay buffer (which retains every
	// published event) is empty for the new room, so no bridge could drive an
	// agent from the imported agent-addressed chat.
	sub := e.rt.Hub.Subscribe(res.RoomID)
	defer e.rt.Hub.Unsubscribe(res.RoomID, sub)
	select {
	case ev := <-sub.Events():
		t.Fatalf("import published %s live", ev.Kind)
	default:
	}
	// New chat continues on the same monotonic sequence after the import.
	last := e.history(res.RoomID, "dave", "")
	seq := e.chat(res.RoomID, "dave", map[string]any{"text": "continuing here"})
	if seq <= last[len(last)-1].Sequence {
		t.Fatalf("post-import seq %d not after imported %d", seq, last[len(last)-1].Sequence)
	}
}

func TestHandoffExportRequiresOwnerOrAdmin(t *testing.T) {
	e := newHandoffEnv(t)
	roomID, _ := seedRoom(e)
	if code, _ := e.do(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/export", "mallory", nil); code != http.StatusForbidden {
		t.Fatalf("non-member export = %d, want 403", code)
	}
	if code, _ := e.do(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/export", "bob", nil); code != http.StatusForbidden {
		t.Fatalf("plain member export = %d, want 403", code)
	}
	if code, _ := e.do(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/export", "jack", nil); code != http.StatusOK {
		t.Fatalf("admin export = %d, want 200", code)
	}
	if code, _ := e.do(e.roomSrv, http.MethodGet, "/v1/rooms/"+roomID+"/handoff", "mallory", nil); code != http.StatusForbidden {
		t.Fatalf("non-member handoff read = %d, want 403", code)
	}
}

func validManifest() []byte {
	b, _ := json.Marshal(wire.RoomExportManifest{
		Format: wire.RoomExportFormat, ExportedAtMs: 1, ExportedBy: "alice",
		Room:     wire.RoomExportRoom{ID: "src", Name: "Source"},
		Channels: []wire.RoomExportChannel{{ID: "ch_a", Name: "general", IsDefault: true}},
		Members:  []wire.RoomExportMember{{UserID: "alice", Role: "owner"}},
	})
	return b
}

func TestHandoffImportRejectsBadPackages(t *testing.T) {
	e := newHandoffEnv(t)
	chat := func(seq int64, text string) string {
		return fmt.Sprintf(`{"sequence":%d,"channelId":"ch_a","kind":"chat_message","payload":{"userId":"alice","text":%q},"atUnixMs":1}`, seq, text)
	}
	cases := []struct {
		name  string
		files map[string][]byte
		want  string
	}{
		{"wrong format", map[string][]byte{
			"manifest.json":  []byte(`{"format":"bnbloc.room-export/v2","room":{"id":"src"}}`),
			"messages.jsonl": nil,
		}, "unsupported package format"},
		{"no manifest", map[string][]byte{"messages.jsonl": nil}, "manifest.json is missing"},
		{"no messages", map[string][]byte{"manifest.json": validManifest()}, "messages.jsonl is missing"},
		{"bad room id", map[string][]byte{
			"manifest.json":  []byte(`{"format":"` + wire.RoomExportFormat + `","room":{"id":"a/b"}}`),
			"messages.jsonl": nil,
		}, "not a valid room id"},
		{"unknown kind", map[string][]byte{
			"manifest.json":  validManifest(),
			"messages.jsonl": []byte(`{"sequence":1,"kind":"presence_join","payload":{}}`),
		}, "unsupported kind"},
		{"out of order", map[string][]byte{
			"manifest.json":  validManifest(),
			"messages.jsonl": []byte(chat(2, "b") + "\n" + chat(1, "a")),
		}, "ascending sequence"},
		{"unknown channel", map[string][]byte{
			"manifest.json":  validManifest(),
			"messages.jsonl": []byte(`{"sequence":1,"channelId":"ch_x","kind":"chat_message","payload":{}}`),
		}, "unknown channel"},
		{"line too long", map[string][]byte{
			"manifest.json":  validManifest(),
			"messages.jsonl": []byte(chat(1, strings.Repeat("x", maxHandoffLineBytes))),
		}, "exceeds"},
		{"artifact over cap", map[string][]byte{
			"manifest.json":                 validManifest(),
			"messages.jsonl":                nil,
			"artifacts/art_1/artifact.json": []byte(`{"artifactId":"art_1","kind":"markdown","title":"T","versions":[{"version":1,"file":"artifacts/art_1/v1.md"}]}`),
			"artifacts/art_1/v1.md":         bytes.Repeat([]byte("x"), maxArtifactContentBytes+1),
		}, "exceeds"},
		{"artifact hash mismatch", map[string][]byte{
			"manifest.json":                 validManifest(),
			"messages.jsonl":                nil,
			"artifacts/art_1/artifact.json": []byte(`{"artifactId":"art_1","kind":"markdown","title":"T","versions":[{"version":1,"contentHash":"00","file":"artifacts/art_1/v1.md"}]}`),
			"artifacts/art_1/v1.md":         []byte("# hi"),
		}, "does not match its hash"},
		{"path traversal", map[string][]byte{
			"manifest.json":  validManifest(),
			"messages.jsonl": nil,
			"../evil":        []byte("x"),
		}, "invalid archive entry name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := e.importPackage("dave", writeZip(t, tc.files))
			if code != http.StatusBadRequest || !strings.Contains(string(out), tc.want) {
				t.Fatalf("import = %d %s, want 400 containing %q", code, out, tc.want)
			}
		})
	}
	if code, out := e.importPackage("dave", []byte("definitely not a zip")); code != http.StatusBadRequest || !strings.Contains(string(out), "not a zip") {
		t.Fatalf("garbage import = %d %s", code, out)
	}
	// A refused package creates nothing.
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	if rooms := e.rooms.RoomsForUser(ctx, "dave"); len(rooms) != 0 {
		t.Fatalf("rejected imports left rooms behind: %v", rooms)
	}
}

func TestHandoffImportSkipsRefusedAttachment(t *testing.T) {
	e := newHandoffEnv(t)
	files := map[string][]byte{
		"manifest.json": validManifest(),
		"messages.jsonl": []byte(`{"sequence":1,"channelId":"ch_a","kind":"chat_message","payload":{"userId":"alice","text":"see file",` +
			`"attachments":[{"id":"att_1","filename":"page.html","contentType":"text/html","size":5}]},"atUnixMs":7}`),
		"attachments/att_1/attachment.json": []byte(`{"id":"att_1","filename":"page.html","contentType":"text/html","size":5,"file":"attachments/att_1/page.html"}`),
		"attachments/att_1/page.html":       []byte("<b>x</b>"),
	}
	code, out := e.importPackage("dave", writeZip(t, files))
	if code != http.StatusOK {
		t.Fatalf("import = %d %s", code, out)
	}
	var res wire.RoomImportResult
	_ = json.Unmarshal(out, &res)
	if !reflect.DeepEqual(res.SkippedAttachments, []string{"att_1"}) || res.ImportedMessages != 1 {
		t.Fatalf("result = %+v", res)
	}
	msgs := e.history(res.RoomID, "dave", "")
	if len(msgs) != 1 || bytes.Contains(msgs[0].Payload, []byte("att_1")) || msgs[0].AtUnixMs != 7 {
		t.Fatalf("history = %+v", msgs)
	}
}
