package wire

// SearchReindexRequest is the body of POST /v1/platform/search/reindex
// (super-admin, served by realtime): continue the search backfill after
// Cursor ("" = from the start), processing at most MaxBatches batches
// (0 = the server default; the server also caps one call's wall time).
type SearchReindexRequest struct {
	Cursor     string `json:"cursor,omitempty"`
	MaxBatches int    `json:"maxBatches,omitempty"`
}

// SearchReindexResponse is one call's progress: rows read and rows whose
// search text was filled in this call, the cursor to pass to the next call,
// and whether the whole feed has been walked.
type SearchReindexResponse struct {
	Scanned int    `json:"scanned"`
	Updated int    `json:"updated"`
	Cursor  string `json:"cursor"`
	Done    bool   `json:"done"`
}
