package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// A person who has not joined a previewable room reads it through the whole
// stack (gateway edge list, room service, realtime) and nothing else: the
// gateway's preview list and the backends' opt-ins agree
// (docs/prd/2026-10-22-shared-room-preview.md). Visibility does not matter
// (a private room can be previewable, a shared one not); the switch does.
func TestE2EPreviewOfAPreviewableRoomAcrossTheServices(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		id          string
		visibility  store.Visibility
		previewable bool
	}{
		{"room-1", store.VisibilityShared, true},
		{"room-open", store.VisibilityPrivate, true},
		{"room-priv", store.VisibilityPrivate, false},
		{"room-closed", store.VisibilityShared, false},
	} {
		if err := h.rooms.CreateRoomWithOwnerAndChannel(tctx(), store.Room{RoomID: c.id, Name: c.id, Visibility: c.visibility, Previewable: c.previewable, CreatedBy: "alice", CreatedAtMs: 1},
			store.Membership{RoomID: c.id, UserID: "alice", Role: store.RoleOwner, JoinedAtMs: 1},
			store.RoomChannel{ChannelID: "ch_" + c.id, RoomID: c.id, Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	alice, jack := h.login(t, "alice"), h.login(t, "jack")
	do := func(token, method, path, body string) (int, []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, method, h.gateway.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		if strings.HasSuffix(path, "/events") {
			return resp.StatusCode, nil // a stream: the status is the answer
		}
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}

	status, raw := do(alice, http.MethodPost, "/v1/rooms/room-1/artifacts", `{"kind":"markdown","title":"Spec","content":"# hi"}`)
	var created struct {
		Artifact struct {
			ArtifactID string `json:"artifactId"`
		} `json:"artifact"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &created) != nil || created.Artifact.ArtifactID == "" {
		t.Fatalf("seed artifact: %d %s", status, raw)
	}
	art := created.Artifact.ArtifactID

	reads := []string{
		"/v1/rooms/room-1/channels",
		"/v1/rooms/room-1/members",
		"/v1/rooms/room-1/history?channelId=ch_room-1",
		"/v1/rooms/room-1/events?channelId=ch_room-1",
		"/v1/rooms/room-1/artifacts",
		"/v1/rooms/room-1/artifact-anchors?from=1&to=9",
		"/v1/rooms/room-1/artifacts/" + art,
		"/v1/rooms/room-1/artifacts/" + art + "/state",
		"/v1/rooms/room-1/artifacts/" + art + "/versions/1",
		"/v1/rooms/room-1/artifacts/" + art + "/versions/1/content",
	}
	for _, path := range reads {
		for _, room := range []string{"room-1", "room-open"} {
			other := strings.ReplaceAll(path, "room-1", room)
			if room == "room-open" && strings.Contains(path, "/artifacts/") {
				continue // the seeded artifact lives in room-1
			}
			if status, _ := do(jack, http.MethodGet, other, ""); status != http.StatusOK && status != http.StatusNoContent {
				t.Errorf("GET %s as a non-member of a previewable room = %d, want 200", other, status)
			}
		}
		for _, room := range []string{"room-priv", "room-closed"} {
			other := strings.ReplaceAll(path, "room-1", room)
			if status, _ := do(jack, http.MethodGet, other, ""); status != http.StatusForbidden {
				t.Errorf("GET %s as a non-member of a room that is not previewable = %d, want 403", other, status)
			}
		}
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/rooms/room-1/chat", `{"text":"hi","channelId":"ch_room-1"}`},
		{http.MethodPost, "/v1/rooms/room-1/channels", `{"name":"x"}`},
		{http.MethodPost, "/v1/rooms/room-1/artifacts", `{"kind":"markdown","title":"x","content":"x"}`},
		{http.MethodPost, "/v1/rooms/room-1/artifacts/" + art + "/versions", `{"baseVersion":1,"content":"x"}`},
		{http.MethodGet, "/v1/rooms/room-1/agents", ""},
		{http.MethodGet, "/v1/rooms/room-1/tasks", ""},
		{http.MethodGet, "/v1/rooms/room-1/people-chat", ""},
	} {
		if status, _ := do(jack, c.method, c.path, c.body); status != http.StatusForbidden {
			t.Errorf("%s %s as a preview = %d, want 403", c.method, c.path, status)
		}
	}
}
