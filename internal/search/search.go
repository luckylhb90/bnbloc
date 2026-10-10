// Package search is the message search backend seam
// (docs/prd/2026-10-10-enterprise-scale.md §4). Global search
// (GET /v1/search, served by realtime) asks an Index for the room messages
// a query matches; the Index answers with store rows only, so the store
// stays the authority over what exists and what a caller may see.
//
// Two implementations:
//
//   - Store (the default): the store's own text index. MySQL keeps
//     room_messages.search_text in the same statement as the row, with a
//     FULLTEXT (ngram) index; the in-memory dev store matches substrings.
//     Nothing to keep current: edits, deletes and retention deletes change
//     the row and the index with it.
//   - OpenSearch (BNBLOC_SEARCH_OPENSEARCH_URL): an external index over the
//     same rows. It returns candidate keys with the scope filters applied in
//     the query; every candidate is then re-read from the store and
//     re-checked with the exact predicates of the MySQL query
//     (store.MessageSearch.Admits). The index is kept current
//     asynchronously by an Updater fed from the room's group commit, edits
//     and deletes, and by retention (PurgeBefore). A failed update is logged
//     and counted and never fails or delays a write; `bnbloc search reindex`
//     repairs any drift.
package search

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Backend names.
const (
	BackendMySQL      = "mysql"
	BackendOpenSearch = "opensearch"
)

// Index is a message search backend.
type Index interface {
	// Backend names the implementation (BackendMySQL, BackendOpenSearch).
	Backend() string
	// Search returns the live messages of q.RoomIDs in the context's tenant
	// whose text matches q.Text, newest first (created_at_ms, then seq), at
	// most q.PageSize(). The caller resolves q.RoomIDs from membership; an
	// Index never widens it. An unsearchable query (q.Searchable) has no
	// results.
	Search(ctx context.Context, q store.MessageSearch) ([]store.RoomMessage, error)
	// Reindex brings the index up to date for one batch of the room feed
	// after cursor (primary-key order, cross-tenant), reporting the cursor
	// to continue from. It is idempotent and safe while serving.
	Reindex(ctx context.Context, cursor string, batch int) (store.SearchReindexBatch, error)
	// Apply brings the index up to date for the given messages, re-read
	// from the store (a missing row removes its document). The Updater
	// calls it; the Store index has nothing to do.
	Apply(ctx context.Context, keys []store.MessageKey) error
	// PurgeBefore removes the documents of tenantID's messages created
	// before beforeMs: data retention deleted (or fenced) every such row.
	// It returns how many documents it removed.
	PurgeBefore(ctx context.Context, tenantID string, beforeMs int64) (int64, error)
	// Status reports the backend's health for the platform status route.
	Status(ctx context.Context) wire.SearchStatus
}

// Store is the default Index: the store's own text index (see the package
// doc).
type Store struct {
	Chat store.ChatIndex
}

// NewStore returns the store-backed index over chat.
func NewStore(chat store.ChatIndex) *Store { return &Store{Chat: chat} }

var _ Index = (*Store)(nil)

// Backend is BackendMySQL (the in-memory dev store answers the same way).
func (s *Store) Backend() string { return BackendMySQL }

// Search runs the store's FULLTEXT (ngram) phrase query.
func (s *Store) Search(ctx context.Context, q store.MessageSearch) ([]store.RoomMessage, error) {
	return s.Chat.SearchRoomMessages(ctx, q)
}

// Reindex fills room_messages.search_text for rows stored without it.
func (s *Store) Reindex(ctx context.Context, cursor string, batch int) (store.SearchReindexBatch, error) {
	return s.Chat.ReindexSearchText(ctx, cursor, batch)
}

// Apply has nothing to do: the row write is the index write.
func (s *Store) Apply(context.Context, []store.MessageKey) error { return nil }

// PurgeBefore has nothing to do: the FULLTEXT index follows the rows.
func (s *Store) PurgeBefore(context.Context, string, int64) (int64, error) { return 0, nil }

// Status is always ready: the index lives in the database.
func (s *Store) Status(context.Context) wire.SearchStatus {
	return wire.SearchStatus{Backend: BackendMySQL, Ready: true}
}
