package room

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// previewEnv is a room service with a shared room "pub" and a private room
// "priv", each owned by alice with one markdown deliverable; "jack" is in
// neither (docs/prd/2026-10-22-shared-room-preview.md).
type previewEnv struct {
	srv       *httptest.Server
	artifacts map[string]string // room -> artifact id
}

func newPreviewEnv(t *testing.T) *previewEnv {
	t.Helper()
	mem := store.NewMemoryRoom()
	svc := New(mem, mem.Channels())
	svc.Artifacts = store.NewMemoryArtifacts()
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	e := &previewEnv{srv: srv, artifacts: map[string]string{}}
	for room, visibility := range map[string]string{"pub": "shared", "priv": "private"} {
		resp := artifactRequest(t, srv, http.MethodPost, "/v1/rooms", "alice", map[string]any{"roomId": room, "name": room, "visibility": visibility})
		resp.Body.Close()
		body := decodeJSON(t, artifactRequest(t, srv, http.MethodPost, "/v1/rooms/"+room+"/artifacts", "alice", map[string]any{
			"kind": "markdown", "title": "Spec", "content": "# hello",
		}))
		e.artifacts[room] = body["artifact"].(map[string]any)["artifactId"].(string)
	}
	return e
}

func (e *previewEnv) status(t *testing.T, method, path, user string) int {
	t.Helper()
	resp := artifactRequest(t, e.srv, method, path, user, nil)
	resp.Body.Close()
	return resp.StatusCode
}

// A non-member reads a shared room's channels, members and deliverables, and
// nothing of a private room.
func TestSharedRoomIsReadableByANonMember(t *testing.T) {
	e := newPreviewEnv(t)
	art := e.artifacts["pub"]
	for _, path := range []string{
		"/v1/rooms/pub/channels",
		"/v1/rooms/pub/members",
		"/v1/rooms/pub/artifacts",
		"/v1/rooms/pub/artifacts/" + art,
		"/v1/rooms/pub/artifacts/" + art + "/versions/1",
	} {
		if got := e.status(t, http.MethodGet, path, "jack"); got != http.StatusOK {
			t.Errorf("GET %s by a non-member of a shared room = %d, want 200", path, got)
		}
	}
	// The content route is read too (a markdown version is inline text).
	if got := e.status(t, http.MethodGet, "/v1/rooms/pub/artifacts/"+art+"/versions/1/content", "jack"); got != http.StatusOK {
		t.Errorf("content by a non-member of a shared room = %d, want 200", got)
	}
	priv := e.artifacts["priv"]
	for _, path := range []string{
		"/v1/rooms/priv/channels",
		"/v1/rooms/priv/members",
		"/v1/rooms/priv/artifacts",
		"/v1/rooms/priv/artifacts/" + priv,
		"/v1/rooms/priv/artifacts/" + priv + "/versions/1",
		"/v1/rooms/priv/artifacts/" + priv + "/versions/1/content",
	} {
		if got := e.status(t, http.MethodGet, path, "jack"); got != http.StatusForbidden {
			t.Errorf("GET %s by a non-member of a private room = %d, want 403", path, got)
		}
	}
	// An artifact id of another room is not found, never read across rooms.
	if got := e.status(t, http.MethodGet, "/v1/rooms/pub/artifacts/"+priv, "jack"); got != http.StatusNotFound {
		t.Errorf("another room's artifact through a shared room = %d, want 404", got)
	}
}

// Reading is all a non-member of a shared room may do: every write of the
// room service stays members'.
func TestSharedRoomWritesStayMembers(t *testing.T) {
	e := newPreviewEnv(t)
	art := e.artifacts["pub"]
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/rooms/pub/artifacts"},
		{http.MethodPost, "/v1/rooms/pub/artifacts/" + art + "/versions"},
		{http.MethodPut, "/v1/rooms/pub/artifacts/" + art + "/versions/1/thumbnail"},
		{http.MethodPost, "/v1/rooms/pub/channels"},
		{http.MethodPatch, "/v1/rooms/pub/channels/x"},
		{http.MethodDelete, "/v1/rooms/pub/channels/x"},
		{http.MethodPost, "/v1/rooms/pub/members"},
		{http.MethodPost, "/v1/rooms/pub/invites"},
		{http.MethodPost, "/v1/rooms/pub/people-chat"},
		{http.MethodGet, "/v1/rooms/pub/people-chat"},
		{http.MethodGet, "/v1/rooms/pub/handoff"},
		{http.MethodGet, "/v1/rooms/pub/artifacts/" + art + "/state"},
	} {
		if got := e.status(t, c.method, c.path, "jack"); got != http.StatusForbidden {
			t.Errorf("%s %s by a non-member = %d, want 403", c.method, c.path, got)
		}
	}
}

// The membership probe says a non-member of a live shared room may preview
// it, with the room's tenant, and says nothing for a private room.
func TestMembershipProbeAnswersPreview(t *testing.T) {
	e := newPreviewEnv(t)
	probe := func(room, user string) map[string]any {
		return decodeJSON(t, artifactRequest(t, e.srv, http.MethodGet, "/v1/rooms/"+room+"/membership/"+user, user, nil))
	}
	if got := probe("pub", "jack"); got["member"] != false || got["preview"] != true || got["tenantId"] == "" || got["tenantId"] == nil {
		t.Fatalf("probe of a shared room by a non-member = %v, want preview with a tenant", got)
	}
	if got := probe("priv", "jack"); got["member"] != false || got["preview"] != nil {
		t.Fatalf("probe of a private room by a non-member = %v, want no preview", got)
	}
	if got := probe("pub", "alice"); got["member"] != true || got["preview"] != nil {
		t.Fatalf("probe of a shared room by its owner = %v, want a membership, no preview", got)
	}
	if got := probe("missing", "jack"); got["member"] != false || got["preview"] != nil {
		t.Fatalf("probe of a missing room = %v, want no preview", got)
	}
}
