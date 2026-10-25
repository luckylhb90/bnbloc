package wire

// LibraryCandidates is GET /v1/library/candidates?q=<request>&limit=N: the shortlist of the
// member's Library that may fit a request they sent
// (docs/prd/2026-10-25-suggest-what-to-use.md), best first. It is the first stage of a
// suggestion (the second is the organization's decision model): bounded, so the cost of a
// suggestion does not grow with the Library. The request is the query; it is not stored.
type LibraryCandidates struct {
	Candidates []LibraryCandidate `json:"candidates"`
	// Total is how many components the member may use (the shortlist is a part of them).
	Total int `json:"total"`
}

// LibraryCandidate is one component as a decision needs it, nothing more.
type LibraryCandidate struct {
	// ID is a content component's id; empty for an MCP tool (named by Name).
	ID string `json:"id,omitempty"`
	// Kind is "expert", "skill" or "mcp".
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Wears is, for an expert, the skill packs it requires (names, no ranges).
	Wears []string `json:"wears,omitempty"`
	// Version is the version a skill pack would be turned on at.
	Version string `json:"version,omitempty"`
}

// Library candidate bounds.
const (
	// MaxLibraryRequestRunes bounds the query: a request longer than this is cut.
	MaxLibraryRequestRunes = 1200
	// MaxLibraryDescriptionRunes trims each candidate's description.
	MaxLibraryDescriptionRunes = 160
)
