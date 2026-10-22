package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// previewGateway is a gateway whose caller "alice" belongs to no room, over a
// membership set with a shared room, a private one, and a shared room of
// another organization. Backends echo "reached" with the asserted user.
func previewGateway(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "reached "+r.Header.Get("X-Bnbloc-User"))
	}))
	t.Cleanup(backend.Close)
	dir := membership.NewStatic()
	for room, tenantID := range map[string]string{"pub": tenant.Default, "priv": tenant.Default, "pub-other": "org-other"} {
		dir.SetRoomTenant(room, tenantID)
		dir.Add(room, "maya")
	}
	dir.SetPreviewable("pub", true)
	dir.SetPreviewable("pub-other", true)
	gw := newGateway(t, backend.URL)
	gw.Backends = Backends{Room: backend.URL, Realtime: backend.URL, Activity: backend.URL, ShareAgent: backend.URL}
	gw.Rooms = func(string, string) []string { return nil }
	gw.Membership = dir
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	token, _ := login(t, srv, "good")
	return srv, token
}

func previewStatus(t *testing.T, srv *httptest.Server, token, method, path string) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A non-member reads a shared room of their organization through exactly the
// preview list, GET only: the public channels and members, history, the
// stream and the deliverables (docs/prd/2026-10-22-shared-room-preview.md).
func TestPreviewReadsASharedRoomThroughTheListOnly(t *testing.T) {
	srv, token := previewGateway(t)
	for _, path := range []string{
		"/v1/rooms/pub/channels",
		"/v1/rooms/pub/members",
		"/v1/rooms/pub/history?channelId=c1",
		"/v1/rooms/pub/artifacts",
		"/v1/rooms/pub/artifact-anchors?from=1&to=9",
		"/v1/rooms/pub/artifacts/a1",
		"/v1/rooms/pub/artifacts/a1/state",
		"/v1/rooms/pub/artifacts/a1/versions/1",
		"/v1/rooms/pub/artifacts/a1/versions/1/content",
		"/v1/rooms/pub/artifacts/a1/versions/1/thumbnail",
	} {
		if got := previewStatus(t, srv, token, http.MethodGet, path); got != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (a preview reads it)", path, got)
		}
	}
	// Everything else of a shared room's sub-resources stays members': reads
	// off the list, and every write. (The room itself answers a non-member as
	// it always did: the room service decides, and refuses a non-owner.)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/rooms/pub/agents"},
		{http.MethodGet, "/v1/rooms/pub/tasks"},
		{http.MethodGet, "/v1/rooms/pub/handoff"},
		{http.MethodGet, "/v1/rooms/pub/people-chat"},
		{http.MethodGet, "/v1/rooms/pub/shared-agents"},
		{http.MethodGet, "/v1/rooms/pub/platform-agents"},
		{http.MethodGet, "/v1/rooms/pub/agent-catalogs"},
		{http.MethodGet, "/v1/rooms/pub/agent-jobs"},
		{http.MethodGet, "/v1/rooms/pub/live"},
		{http.MethodGet, "/v1/rooms/pub/messages"},
		{http.MethodGet, "/v1/rooms/pub/attachments/x"},
		{http.MethodGet, "/v1/rooms/pub/members/maya"},
		{http.MethodGet, "/v1/rooms/pub/membership/maya"},
		{http.MethodPost, "/v1/rooms/pub/artifacts/a1/state"},
		{http.MethodGet, "/v1/rooms/pub/artifacts/a1/versions/1/files/index.html"},
		{http.MethodPost, "/v1/rooms/pub/chat"},
		{http.MethodPost, "/v1/rooms/pub/typing"},
		{http.MethodPost, "/v1/rooms/pub/read"},
		{http.MethodPost, "/v1/rooms/pub/channels"},
		{http.MethodPost, "/v1/rooms/pub/artifacts"},
		{http.MethodPost, "/v1/rooms/pub/artifacts/a1/versions"},
		{http.MethodPut, "/v1/rooms/pub/artifacts/a1/versions/1/thumbnail"},
		{http.MethodDelete, "/v1/rooms/pub/channels/c1"},
		{http.MethodPost, "/v1/rooms/pub/members"},
		{http.MethodPost, "/v1/rooms/pub/schedules"},
	} {
		if got := previewStatus(t, srv, token, c.method, c.path); got != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403 (members only)", c.method, c.path, got)
		}
	}
}

// A room that is private, or shared in another organization, is not readable
// as a preview.
func TestPreviewNeedsASharedRoomOfTheCallersOrganization(t *testing.T) {
	srv, token := previewGateway(t)
	for _, room := range []string{"priv", "pub-other", "missing"} {
		if got := previewStatus(t, srv, token, http.MethodGet, "/v1/rooms/"+room+"/history"); got != http.StatusForbidden {
			t.Errorf("history of %s = %d, want 403", room, got)
		}
	}
}

// A directory without previews (no Previewer) leaves a shared room closed.
func TestPreviewFailsClosedWithoutAPreviewer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	gw.Rooms = func(string, string) []string { return nil }
	gw.Membership = memberOf(func(string, string) bool { return false })
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")
	if got := previewStatus(t, srv, token, http.MethodGet, "/v1/rooms/pub/history"); got != http.StatusForbidden {
		t.Fatalf("history without a Previewer = %d, want 403", got)
	}
}
