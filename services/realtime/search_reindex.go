package realtimehttp

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Search backfill (batch 3, R7): POST /v1/platform/search/reindex fills the
// search text of messages stored before global search existed. The gateway
// gates /v1/platform/* to super-admins (PermManagePlatform) and routes this
// path here, the owner of room_messages. One call processes a bounded number
// of batches and a bounded wall time, so it stays well inside proxy timeouts;
// the caller (`bnbloc search reindex`) loops on the returned cursor until done.

const (
	defaultReindexBatches = 10
	maxReindexBatches     = 50
	// reindexCallBudget bounds one call's wall time: the batch in flight
	// finishes, then the call answers with its cursor.
	reindexCallBudget = 10 * time.Second
)

func (s *Service) handleSearchReindex(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireSubject(w, r)
	if !ok {
		return
	}
	if s.Messages == nil {
		obs.ServerError(w, "message store unavailable", nil)
		http.Error(w, "message store unavailable", http.StatusServiceUnavailable)
		return
	}
	var req wire.SearchReindexRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	batches := req.MaxBatches
	if batches <= 0 {
		batches = defaultReindexBatches
	}
	if batches > maxReindexBatches {
		batches = maxReindexBatches
	}
	ctx := r.Context()
	deadline := time.Now().Add(reindexCallBudget)
	resp := wire.SearchReindexResponse{Cursor: req.Cursor}
	for i := 0; i < batches && !resp.Done && time.Now().Before(deadline); i++ {
		b, err := s.Messages.ReindexSearchText(ctx, resp.Cursor, store.SearchReindexBatchSize)
		if errors.Is(err, store.ErrInvalidSearchCursor) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			log.Printf("realtime: search reindex by %s at cursor %q: %v", userID, resp.Cursor, err)
			http.Error(w, "search reindex failed", http.StatusInternalServerError)
			return
		}
		resp.Scanned += b.Scanned
		resp.Updated += b.Updated
		resp.Cursor = b.Cursor
		resp.Done = b.Done
	}
	log.Printf("realtime: search reindex by %s: scanned=%d updated=%d cursor=%q done=%v",
		userID, resp.Scanned, resp.Updated, resp.Cursor, resp.Done)
	writeJSON(w, http.StatusOK, resp)
}
