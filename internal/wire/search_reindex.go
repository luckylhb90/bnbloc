package wire

// SearchReindexRequest is the body of POST /v1/platform/search/reindex
// (super-admin, served by realtime): continue the search reindex after
// Cursor ("" = from the start), processing at most MaxBatches batches
// (0 = the server default; the server also caps one call's wall time).
type SearchReindexRequest struct {
	Cursor     string `json:"cursor,omitempty"`
	MaxBatches int    `json:"maxBatches,omitempty"`
}

// SearchReindexResponse is one call's progress: the search backend, rows
// read and rows brought into the index in this call (mysql: search text
// filled for rows stored without it; opensearch: documents written), the
// cursor to pass to the next call, and whether the whole feed has been
// walked.
type SearchReindexResponse struct {
	Backend string `json:"backend"`
	Scanned int    `json:"scanned"`
	Updated int    `json:"updated"`
	Cursor  string `json:"cursor"`
	Done    bool   `json:"done"`
}

// SearchStatus is GET /v1/platform/search/status (super-admin, served by
// realtime): the search backend and its health as the answering replica
// sees it.
type SearchStatus struct {
	// Backend is "mysql" (the store's FULLTEXT index) or "opensearch".
	Backend string `json:"backend"`
	// Index is the OpenSearch index name (opensearch only).
	Index string `json:"index,omitempty"`
	// Ready is false while the backend is unreachable or its index is
	// missing and cannot be created.
	Ready bool `json:"ready"`
	// Health is the index's cluster health (green, yellow, red; opensearch
	// only).
	Health string `json:"health,omitempty"`
	// Documents is the index's document count (opensearch only).
	Documents *int64 `json:"documents,omitempty"`
	// Error explains a not-ready backend.
	Error string `json:"error,omitempty"`
	// Updates is the answering replica's index update queue (opensearch
	// only: the store index needs none).
	Updates *SearchUpdateStats `json:"updates,omitempty"`
}

// SearchUpdateStats counts one replica's asynchronous index updates since
// it started (messages, not batches).
type SearchUpdateStats struct {
	Queued  int   `json:"queued"`
	Applied int64 `json:"applied"`
	Failed  int64 `json:"failed"`
	Dropped int64 `json:"dropped"`
}
