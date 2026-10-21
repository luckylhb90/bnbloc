package room

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The deleted-rooms question answers only on the deletion marker: a deleted
// room is named whoever asks, a live or unknown one never is; it needs a
// caller and takes at most maxDeletedRoomsQuery ids.
func TestDeletedRoomsAnswersOnlyDeletions(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, id := range []string{"room-live", "room-gone"} {
		if r := do(t, http.MethodPost, srv.URL+"/v1/rooms", "maya", map[string]string{"roomId": id, "name": id}); r.StatusCode != http.StatusOK {
			t.Fatalf("create %s = %d", id, r.StatusCode)
		}
	}
	if r := do(t, http.MethodDelete, srv.URL+"/v1/rooms/room-gone", "maya", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", r.StatusCode)
	}
	for _, user := range []string{"maya", "jack"} {
		var got wire.DeletedRooms
		decodeBody(t, do(t, http.MethodGet, srv.URL+"/v1/my/deleted-rooms?ids=room-live,room-gone,room-never", user, nil), &got)
		if len(got.Deleted) != 1 || got.Deleted[0] != "room-gone" {
			t.Fatalf("%s: deleted = %v, want only room-gone", user, got.Deleted)
		}
	}
	if r := do(t, http.MethodGet, srv.URL+"/v1/my/deleted-rooms?ids=room-gone", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a caller = %d, want 401", r.StatusCode)
	}
	ids := make([]string, 0, maxDeletedRoomsQuery+1)
	for i := 0; i <= maxDeletedRoomsQuery; i++ {
		ids = append(ids, "room-"+strconv.Itoa(i))
	}
	if r := do(t, http.MethodGet, srv.URL+"/v1/my/deleted-rooms?ids="+strings.Join(ids, ","), "maya", nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("%d ids = %d, want 400", len(ids), r.StatusCode)
	}
}
