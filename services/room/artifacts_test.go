package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// capturePublisher records events for assertions.
type capturePublisher struct{ events []wire.Event }

func (c *capturePublisher) Publish(_ context.Context, ev wire.Event) { c.events = append(c.events, ev) }

// newArtifactServer builds a room service with the in-memory stores, one room
// with member "alice", and an event capture.
func newArtifactServer(t *testing.T) (*httptest.Server, *capturePublisher) {
	t.Helper()
	mem := store.NewMemoryRoom()
	if err := mem.CreateRoom(testCtx(t), store.Room{RoomID: "room-1", Name: "r", CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	if err := mem.AddMember(testCtx(t), store.Membership{RoomID: "room-1", UserID: "alice", Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	pub := &capturePublisher{}
	svc := New(mem, mem.Channels())
	svc.Artifacts = store.NewMemoryArtifacts()
	svc.Publisher = pub
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return srv, pub
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	return tenant.NewContext(context.Background(), tenant.Default)
}

func artifactRequest(t *testing.T, srv *httptest.Server, method, path, user string, body any) *http.Response {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rd = bytes.NewReader(buf)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-Bnbloc-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

// A member creates a document and its first version lands in one step; the
// room is notified so every open preview refreshes.
func TestCreateArtifactPublishesFirstVersionAndEvent(t *testing.T) {
	srv, pub := newArtifactServer(t)
	resp := artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts", "alice", map[string]any{
		"kind": "markdown", "title": "Q4 Launch Spec", "content": "# Q4\nship it", "summary": "initial draft",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	art := body["artifact"].(map[string]any)
	if art["currentVersion"].(float64) != 1 {
		t.Fatalf("currentVersion = %v, want 1 after create-with-content", art["currentVersion"])
	}
	if len(pub.events) != 1 || pub.events[0].Kind != "artifact_updated" || pub.events[0].RoomID != "room-1" {
		t.Fatalf("events = %+v, want one artifact_updated in room-1", pub.events)
	}
	// The content is retrievable as version 1.
	id := art["artifactId"].(string)
	vr := decodeJSON(t, artifactRequest(t, srv, "GET", "/v1/rooms/room-1/artifacts/"+id+"/versions/1", "alice", nil))
	v := vr["version"].(map[string]any)
	if v["content"].(string) != "# Q4\nship it" || v["authorUserId"].(string) != "alice" {
		t.Fatalf("version = %+v", v)
	}
}

// Concurrent edits must not silently interleave: the second publisher is
// refused with the current version, so it re-reads and rebases. For specs and
// code a visible conflict beats a woven one.
func TestPublishVersionConflictReturns409(t *testing.T) {
	srv, _ := newArtifactServer(t)
	body := decodeJSON(t, artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts", "alice", map[string]any{
		"kind": "code", "title": "service.go", "content": "v1", "language": "go",
	}))
	id := body["artifact"].(map[string]any)["artifactId"].(string)

	first := artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts/"+id+"/versions", "alice", map[string]any{"baseVersion": 1, "content": "v2-a", "summary": "a"})
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first publish = %d", first.StatusCode)
	}
	first.Body.Close()

	stale := artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts/"+id+"/versions", "alice", map[string]any{"baseVersion": 1, "content": "v2-b", "summary": "b"})
	defer stale.Body.Close()
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale publish = %d, want 409", stale.StatusCode)
	}
	// The winner's content is what stands; the loser's write must not have landed.
	vr := decodeJSON(t, artifactRequest(t, srv, "GET", "/v1/rooms/room-1/artifacts/"+id+"/versions/2", "alice", nil))
	if got := vr["version"].(map[string]any)["content"].(string); got != "v2-a" {
		t.Fatalf("version 2 content = %q, want the first writer's v2-a", got)
	}
}

// Artifacts are room-scoped: a member of one room cannot read another room's
// artifact by id, and a non-member gets nothing at all.
func TestArtifactAccessIsRoomScoped(t *testing.T) {
	srv, _ := newArtifactServer(t)
	body := decodeJSON(t, artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts", "alice", map[string]any{
		"kind": "markdown", "title": "secret", "content": "x",
	}))
	id := body["artifact"].(map[string]any)["artifactId"].(string)

	// Non-member: 403.
	if resp := artifactRequest(t, srv, "GET", "/v1/rooms/room-1/artifacts/"+id, "mallory", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member get = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	// Member of the owning room probing via a different room path is refused
	// (403: not a member there; 404 if the room exists but the artifact is not
	// its). Either way the artifact must not leak.
	if resp := artifactRequest(t, srv, "GET", "/v1/rooms/room-other/artifacts/"+id, "alice", nil); resp.StatusCode == http.StatusOK {
		t.Fatal("cross-room get leaked the artifact")
	} else {
		resp.Body.Close()
	}
}

// Content is bounded: one publish cannot turn the preview surface into bulk
// storage.
func TestArtifactContentCapRejectsOversize(t *testing.T) {
	srv, _ := newArtifactServer(t)
	resp := artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts", "alice", map[string]any{
		"kind": "code", "title": "big", "content": strings.Repeat("x", maxArtifactContentBytes+1),
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize create = %d, want 413", resp.StatusCode)
	}
}

// The live-state relay is room-scoped, bounded, and membership-gated: state is
// the artifact's live session (transient fanout, never a revision), so only a
// member of the owning room may publish it, the artifact must actually live in
// that room, and a runaway payload is refused before it reaches the stream.
func TestArtifactStateRelayRules(t *testing.T) {
	srv, pub := newArtifactServer(t)
	body := decodeJSON(t, artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts", "alice", map[string]any{
		"kind": "app", "title": "collab canvas", "content": "<canvas></canvas>",
	}))
	id := body["artifact"].(map[string]any)["artifactId"].(string)

	// Non-member refused.
	if resp := artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts/"+id+"/state", "mallory", map[string]any{"state": map[string]any{"x": 1}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member state = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	// Oversize refused.
	if resp := artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts/"+id+"/state", "alice", map[string]any{"state": map[string]string{"blob": strings.Repeat("x", 20*1024)}}); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize state = %d, want 413", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	// Cross-room artifact id refused (404, no leak).
	if resp := artifactRequest(t, srv, "POST", "/v1/rooms/room-other/artifacts/"+id+"/state", "alice", map[string]any{"state": map[string]any{}}); resp.StatusCode == http.StatusOK {
		t.Fatal("cross-room state relayed")
	} else {
		resp.Body.Close()
	}
	// Valid relay: published to the room, artifact-scoped, actor-attributed.
	if resp := artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts/"+id+"/state", "alice", map[string]any{"state": map[string]any{"dot": []int{3, 4}}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("valid state = %d, want 200", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	var got *wire.Event
	for i := len(pub.events) - 1; i >= 0; i-- {
		if pub.events[i].Kind == "artifact_state" {
			got = &pub.events[i]
			break
		}
	}
	if got == nil {
		t.Fatal("state relay published no artifact_state event")
	}
	// Room-wide delivery: the relay must NOT carry the artifact's channel —
	// live state is transient, so a channel-filtered miss (viewer in another
	// channel) is unrecoverable. This pins that contract.
	if got.ChannelID != "" {
		t.Fatalf("artifact_state carried channelId %q; live state must deliver room-wide", got.ChannelID)
	}
	p := got.Payload.(map[string]any)
	if p["artifactId"] != id || p["actor"] != "alice" {
		t.Fatalf("event payload = %v", p)
	}
	st, _ := p["state"].(map[string]any)
	if _, ok := st["dot"]; !ok {
		t.Fatalf("state not carried: %v", p)
	}
}

// The join snapshot closes the late-joiner gap: a member who starts the
// preview after others have been interacting still lands on the current
// board, and a member of the wrong room (or a non-member) learns nothing.
func TestArtifactStateSnapshot(t *testing.T) {
	srv, _ := newArtifactServer(t)
	body := decodeJSON(t, artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts", "alice", map[string]any{
		"kind": "app", "title": "snap canvas", "content": "<canvas></canvas>",
	}))
	id := body["artifact"].(map[string]any)["artifactId"].(string)
	get := func(user string) (int, map[string]any) {
		resp := artifactRequest(t, srv, "GET", "/v1/rooms/room-1/artifacts/"+id+"/state", user, nil)
		defer resp.Body.Close()
		var m map[string]any
		if resp.StatusCode == http.StatusOK {
			m = decodeJSON(t, resp)
		}
		return resp.StatusCode, m
	}

	// No live session yet: 204, not an error.
	if code, _ := get("alice"); code != http.StatusNoContent {
		t.Fatalf("pre-relay snapshot = %d, want 204", code)
	}
	// Relay a gesture, then the snapshot returns it with actor attribution.
	if resp := artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts/"+id+"/state", "alice", map[string]any{"state": map[string]any{"dot": []int{7, 8}}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("relay = %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	code, m := get("alice")
	if code != http.StatusOK {
		t.Fatalf("post-relay snapshot = %d, want 200", code)
	}
	if m["actor"] != "alice" {
		t.Fatalf("snapshot actor = %v", m["actor"])
	}
	st, _ := m["state"].(map[string]any)
	if _, ok := st["dot"]; !ok {
		t.Fatalf("snapshot state = %v", m["state"])
	}
	// Non-member cannot read the board either.
	if code, _ := get("mallory"); code != http.StatusForbidden {
		t.Fatalf("non-member snapshot = %d, want 403", code)
	}
}

// GET .../artifact-anchors answers the cards of a message range, members only.
func TestArtifactAnchorsRoute(t *testing.T) {
	srv, _ := newArtifactServer(t)
	body := decodeJSON(t, artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts", "alice", map[string]any{
		"kind": "markdown", "title": "avatar",
	}))
	id := body["artifact"].(map[string]any)["artifactId"].(string)
	if resp := artifactRequest(t, srv, "POST", "/v1/rooms/room-1/artifacts/"+id+"/versions", "alice", map[string]any{
		"baseVersion": 0, "content": "x", "sourceSeq": 7,
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("publish = %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	got := decodeJSON(t, artifactRequest(t, srv, "GET", "/v1/rooms/room-1/artifact-anchors?from=1&to=10", "alice", nil))
	anchors, _ := got["anchors"].([]any)
	if len(anchors) != 1 || anchors[0].(map[string]any)["artifactId"] != id || anchors[0].(map[string]any)["seq"].(float64) != 7 {
		t.Fatalf("anchors = %+v", got)
	}
	for _, tc := range []struct {
		user, query string
		want        int
	}{
		{"mallory", "from=1&to=10", http.StatusForbidden},
		{"alice", "from=0&to=10", http.StatusBadRequest},
		{"alice", "from=5&to=4", http.StatusBadRequest},
		{"alice", "to=4", http.StatusBadRequest},
	} {
		resp := artifactRequest(t, srv, "GET", "/v1/rooms/room-1/artifact-anchors?"+tc.query, tc.user, nil)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %s = %d, want %d", tc.user, tc.query, resp.StatusCode, tc.want)
		}
	}
}
