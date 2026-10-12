package realtimehttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Search reindex and status (super-admin). The gateway gates /v1/platform/*
// to super-admins (PermManagePlatform) and routes these paths here, the
// owner of room_messages and of the search backend.
//
// POST /v1/platform/search/reindex walks the room feed and brings the
// search index up to date for it: on OpenSearch it writes every row's
// current document (a backfill of a new index, or a repair after dropped or
// failed updates); the store index has nothing to do (the row write is the
// index write), so it reports done at once. One call processes a bounded number of batches and a
// bounded wall time, so it stays well inside proxy timeouts; the caller
// (`bnbloc search reindex`) loops on the returned cursor until done.
//
// GET /v1/platform/search/status reports the backend and its health as
// this replica sees it (`bnbloc search status`).

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
	if s.Search == nil {
		obs.ServerError(w, "search backend unavailable", nil)
		httpx.Error(w, "search backend unavailable", http.StatusServiceUnavailable)
		return
	}
	var req wire.SearchReindexRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httpx.Error(w, "invalid body", http.StatusBadRequest)
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
	resp := wire.SearchReindexResponse{Backend: s.Search.Backend(), Cursor: req.Cursor}
	for i := 0; i < batches && !resp.Done && time.Now().Before(deadline); i++ {
		b, err := s.Search.Reindex(ctx, resp.Cursor, store.SearchReindexBatchSize)
		if errors.Is(err, store.ErrInvalidSearchCursor) {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			obs.ServerError(w, "search reindex ("+resp.Backend+")", fmt.Errorf("by %s at cursor %q: %w", userID, resp.Cursor, err))
			httpx.Error(w, "search reindex failed", http.StatusInternalServerError)
			return
		}
		resp.Scanned += b.Scanned
		resp.Updated += b.Updated
		resp.Cursor = b.Cursor
		resp.Done = b.Done
	}
	log.Printf("realtime: search reindex (%s) by %s: scanned=%d updated=%d cursor=%q done=%v",
		resp.Backend, userID, resp.Scanned, resp.Updated, resp.Cursor, resp.Done)
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *Service) handleSearchStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSubject(w, r); !ok {
		return
	}
	if s.Search == nil {
		obs.ServerError(w, "search backend unavailable", nil)
		httpx.Error(w, "search backend unavailable", http.StatusServiceUnavailable)
		return
	}
	st := s.Search.Status(r.Context())
	st.Updates = s.SearchUpdates.Stats()
	httpx.WriteJSON(w, http.StatusOK, st)
}
