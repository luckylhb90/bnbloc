package room

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// previewEnv is a room service with a shared room "pub" and a private room
// "priv", each owned by alice with one markdown deliverable; "jack" is in
// neither (docs/prd/2026-10-22-shared-room-preview.md).
type previewEnv struct {
	mem       *store.MemoryRoom
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
	e := &previewEnv{mem: mem, srv: srv, artifacts: map[string]string{}}
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
		"/v1/rooms/pub/artifact-anchors?from=1&to=9",
		"/v1/rooms/pub/artifacts/" + art + "/state",
	} {
		if got := e.status(t, http.MethodGet, path, "jack"); got != http.StatusOK && got != http.StatusNoContent {
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
		"/v1/rooms/priv/artifact-anchors?from=1&to=9",
		"/v1/rooms/priv/artifacts/" + priv + "/state",
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
		{http.MethodPost, "/v1/rooms/pub/artifacts/" + art + "/state"},
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
		return decodeJSON(t, artifactRequest(t, e.srv, http.MethodGet, "/v1/rooms/"+room+"/membership/"+user+"?preview=1", user, nil))
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
	// Every other lookup of a non-member stays the one membership read: no
	// preview answer unless it asks.
	plain := decodeJSON(t, artifactRequest(t, e.srv, http.MethodGet, "/v1/rooms/pub/membership/jack", "jack", nil))
	if plain["member"] != false || plain["preview"] != nil {
		t.Fatalf("probe without ?preview=1 = %v, want a plain non-member", plain)
	}
}

// The probe of many rooms answers the member's memberships among them,
// leaves out rooms they are not in, and answers only the member themselves.
func TestMembershipsProbe(t *testing.T) {
	e := newPreviewEnv(t)
	got := decodeJSON(t, artifactRequest(t, e.srv, http.MethodGet, "/v1/users/alice/memberships?rooms=pub,priv,missing", "alice", nil))
	rooms, _ := got["rooms"].(map[string]any)
	if len(rooms) != 2 || rooms["pub"] == nil || rooms["priv"] == nil {
		t.Fatalf("alice's memberships = %v, want pub and priv", got)
	}
	if a := rooms["pub"].(map[string]any); a["member"] != true || a["role"] != "owner" || a["tenantId"] == "" {
		t.Fatalf("alice in pub = %v, want an owner with the room's tenant", a)
	}
	if got := decodeJSON(t, artifactRequest(t, e.srv, http.MethodGet, "/v1/users/jack/memberships?rooms=pub,priv", "jack", nil)); len(got["rooms"].(map[string]any)) != 0 {
		t.Fatalf("jack's memberships = %v, want none (a shared room is a preview, not a membership)", got)
	}
	if code := e.status(t, http.MethodGet, "/v1/users/alice/memberships?rooms=pub", "jack"); code != http.StatusForbidden {
		t.Fatalf("jack asking for alice's memberships = %d, want 403", code)
	}
	many := strings.TrimSuffix(strings.Repeat("r,", membership.MaxProbeRooms+1), ",")
	if code := e.status(t, http.MethodGet, "/v1/users/alice/memberships?rooms="+many, "alice"); code != http.StatusBadRequest {
		t.Fatalf("a probe of %d rooms = %d, want 400", membership.MaxProbeRooms+1, code)
	}
}

// A non-member reading a shared room sees its people, not the hosts of its
// platform agents; a member sees both.
func TestSharedRoomMembersListHidesAgentHostsFromANonMember(t *testing.T) {
	e := newPreviewEnv(t)
	if err := e.mem.AddAgentHost(testCtx(t), "pub", "host-1", 1); err != nil {
		t.Fatal(err)
	}
	listed := func(user string) map[string]bool {
		body := decodeJSON(t, artifactRequest(t, e.srv, http.MethodGet, "/v1/rooms/pub/members", user, nil))
		out := map[string]bool{}
		for _, m := range body["members"].([]any) {
			out[m.(map[string]any)["userId"].(string)] = true
		}
		return out
	}
	if got := listed("jack"); got["host-1"] || !got["alice"] {
		t.Fatalf("a non-member sees %v, want alice and no agent host", got)
	}
	if got := listed("alice"); !got["host-1"] || !got["alice"] {
		t.Fatalf("a member sees %v, want alice and the agent host", got)
	}
}

// Readable before joining is the owner's switch, apart from how the room is
// joined: a shared room starts previewable and a private one not, either can
// be set, and a non-member sees the room card of a shared room, or of a
// private room only when it is previewable.
func TestPreviewableIsTheOwnersSwitchApartFromVisibility(t *testing.T) {
	e := newPreviewEnv(t)
	create := func(room, visibility string, previewable *bool) {
		body := map[string]any{"roomId": room, "name": room, "visibility": visibility}
		if previewable != nil {
			body["previewable"] = *previewable
		}
		artifactRequest(t, e.srv, http.MethodPost, "/v1/rooms", "alice", body).Body.Close()
	}
	yes, no := true, false
	create("open", "private", &yes) // private, but readable
	create("closed", "shared", &no) // shared, but not readable

	card := func(room string) (int, map[string]any) {
		resp := artifactRequest(t, e.srv, http.MethodGet, "/v1/rooms/"+room, "jack", nil)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return resp.StatusCode, nil
		}
		return resp.StatusCode, decodeJSON(t, resp)
	}
	for room, want := range map[string]struct {
		status      int
		previewable bool
	}{
		"pub":    {http.StatusOK, true},        // shared: previewable by default
		"open":   {http.StatusOK, true},        // private with the switch on
		"closed": {http.StatusOK, false},       // shared: a card to join, not to read
		"priv":   {http.StatusNotFound, false}, // private by default: nothing confirmed
	} {
		status, got := card(room)
		if status != want.status || (got != nil && got["previewable"] != want.previewable) {
			t.Errorf("card of %s for a non-member = %d %v, want %d previewable=%v", room, status, got, want.status, want.previewable)
		}
		reads := e.status(t, http.MethodGet, "/v1/rooms/"+room+"/channels", "jack")
		if (reads == http.StatusOK) != want.previewable {
			t.Errorf("channels of %s for a non-member = %d, want readable=%v", room, reads, want.previewable)
		}
	}

	// Only the owner flips it, and it takes effect at once.
	patch := func(user string, on bool) int {
		resp := artifactRequest(t, e.srv, http.MethodPatch, "/v1/rooms/closed", user, map[string]any{"previewable": on})
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := patch("jack", true); got != http.StatusForbidden {
		t.Fatalf("a non-member's patch = %d, want 403", got)
	}
	if got := patch("alice", true); got != http.StatusOK {
		t.Fatalf("the owner's patch = %d, want 200", got)
	}
	if got := e.status(t, http.MethodGet, "/v1/rooms/closed/channels", "jack"); got != http.StatusOK {
		t.Fatalf("channels after the switch went on = %d, want 200", got)
	}
	if got := patch("alice", false); got != http.StatusOK || e.status(t, http.MethodGet, "/v1/rooms/closed/channels", "jack") != http.StatusForbidden {
		t.Fatalf("the switch off did not close the room (patch %d)", got)
	}

	// The plaza lists a previewable private room (locked: it is joined with a
	// code) and keeps any other private room redacted.
	plaza := decodeJSON(t, artifactRequest(t, e.srv, http.MethodGet, "/v1/rooms", "jack", nil))
	rows := map[string]map[string]any{}
	for _, r := range plaza["rooms"].([]any) {
		row := r.(map[string]any)
		rows[row["roomId"].(string)] = row
	}
	if r := rows["open"]; r == nil || r["locked"] != true || r["previewable"] != true || r["memberCount"] == nil {
		t.Errorf("plaza row of a previewable private room = %v, want locked, previewable, with its member count", r)
	}
	if r := rows["priv"]; r == nil || r["locked"] != true || r["previewable"] != false || r["memberCount"] != nil {
		t.Errorf("plaza row of a private room = %v, want locked, redacted", r)
	}
	if r := rows["pub"]; r == nil || r["locked"] != false || r["previewable"] != true {
		t.Errorf("plaza row of a shared room = %v, want open and previewable", r)
	}
}
