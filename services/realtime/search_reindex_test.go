package realtimehttp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// POST /v1/platform/search/reindex is super-admin maintenance. On the store
// index there is nothing to walk (search_text is written with the row), so
// it reports done at once; the OpenSearch walk is covered in internal/search.
func TestSearchReindexRouteOnStoreIndexIsDone(t *testing.T) {
	f := newTeamFixture(t)
	f.chatAs(t, "alice", "room-1", "deploy note")

	if code, _ := f.do(t, "", http.MethodPost, "/v1/platform/search/reindex", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("no subject = %d, want 401", code)
	}
	var got wire.SearchReindexResponse
	code, body := f.do(t, "root", http.MethodPost, "/v1/platform/search/reindex", wire.SearchReindexRequest{MaxBatches: 1})
	if code != http.StatusOK {
		t.Fatalf("reindex = %d (%s)", code, body)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Backend != "mysql" || !got.Done || got.Scanned != 0 || got.Updated != 0 {
		t.Fatalf("progress = %+v, want done at once", got)
	}
}
