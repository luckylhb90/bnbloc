package realtimehttp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// R7: POST /v1/platform/search/reindex walks the feed in bounded calls and
// reports progress; a caller loops on the cursor until done.
func TestSearchReindexRouteWalksToDone(t *testing.T) {
	f := newTeamFixture(t)
	for i := 0; i < 3; i++ {
		f.chatAs(t, "alice", "room-1", "deploy note")
	}
	f.chatAs(t, "carol", "room-2", "other room")

	if code, _ := f.do(t, "", http.MethodPost, "/v1/platform/search/reindex", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("no subject = %d, want 401", code)
	}
	if code, _ := f.do(t, "root", http.MethodPost, "/v1/platform/search/reindex", map[string]any{"cursor": "garbage"}); code != http.StatusBadRequest {
		t.Fatalf("bad cursor = %d, want 400", code)
	}

	var got wire.SearchReindexResponse
	code, body := f.do(t, "root", http.MethodPost, "/v1/platform/search/reindex", wire.SearchReindexRequest{MaxBatches: 1})
	if code != http.StatusOK {
		t.Fatalf("reindex = %d (%s)", code, body)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Backend != "mysql" || got.Scanned != 4 || !got.Done || got.Updated != 0 || got.Cursor != "room-2/1" {
		t.Fatalf("progress = %+v, want 4 scanned, done, cursor room-2/1", got)
	}
	// Resuming at the end is a no-op that stays done.
	code, body = f.do(t, "root", http.MethodPost, "/v1/platform/search/reindex", wire.SearchReindexRequest{Cursor: got.Cursor})
	if code != http.StatusOK {
		t.Fatalf("resume = %d (%s)", code, body)
	}
	var again wire.SearchReindexResponse
	_ = json.Unmarshal(body, &again)
	if again.Scanned != 0 || !again.Done || again.Cursor != got.Cursor {
		t.Fatalf("resume progress = %+v", again)
	}
}
