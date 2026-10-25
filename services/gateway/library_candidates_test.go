package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/librarysearch"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func libraryCandidates(t *testing.T, g *contentGateway, user, query string) wire.LibraryCandidates {
	t.Helper()
	code, raw := g.do(user, http.MethodGet, "/v1/library/candidates?"+query, "", nil)
	if code != http.StatusOK {
		t.Fatalf("candidates %q = %d %s", query, code, raw)
	}
	var out wire.LibraryCandidates
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

func saveExpert(t *testing.T, g *contentGateway, user, name, description string, skills ...string) {
	t.Helper()
	form := map[string]any{"kind": "expert", "name": name, "displayName": strings.ToUpper(name[:1]) + name[1:], "description": description,
		"instructions": "Do the work well."}
	if code, raw := g.json(user, http.MethodPost, "/v1/content-components", map[string]any{"form": form}); code != http.StatusCreated {
		t.Fatalf("save %s = %d %s", name, code, raw)
	}
}

// The shortlist is the member's own Library ranked against the request, bounded, with each
// candidate as the few fields a decision needs; nobody else's personal content is in it.
func TestLibraryCandidatesShortlistsTheMembersLibrary(t *testing.T) {
	g := newContentGateway(t)
	saveExpert(t, g, "maya", "invoice-helper", "Reconciles invoices against payments and flags the differences.")
	saveExpert(t, g, "maya", "poem-writer", "Writes short poems on request.")
	saveExpert(t, g, "jack", "secret-planner", "Jack's private planning expert for reconciling invoices.")

	got := libraryCandidates(t, g, "maya", "q="+url.QueryEscape("please reconcile these invoices"))
	if got.Total != 2 {
		t.Fatalf("total = %d: only maya's own two components are hers to use: %+v", got.Total, got.Candidates)
	}
	if len(got.Candidates) == 0 || got.Candidates[0].Name != "invoice-helper" || got.Candidates[0].Kind != "expert" || got.Candidates[0].ID == "" {
		t.Fatalf("the request is placed first: %+v", got.Candidates)
	}
	for _, c := range got.Candidates {
		if c.Name == "secret-planner" {
			t.Fatalf("another member's personal content must never be a candidate: %+v", got.Candidates)
		}
		if len([]rune(c.Description)) > wire.MaxLibraryDescriptionRunes {
			t.Fatalf("a description is trimmed to %d: %d", wire.MaxLibraryDescriptionRunes, len([]rune(c.Description)))
		}
	}
}

func TestLibraryCandidatesAreBoundedWhateverTheLibrarySize(t *testing.T) {
	g := newContentGateway(t)
	for i := 0; i < 130; i++ {
		saveExpert(t, g, "maya", fmt.Sprintf("review-helper-%d", i), "Reviews things carefully and reports what it found.")
	}
	got := libraryCandidates(t, g, "maya", "q=review")
	if got.Total != 130 || len(got.Candidates) != librarysearch.DefaultLimit {
		t.Fatalf("total %d, returned %d (default %d)", got.Total, len(got.Candidates), librarysearch.DefaultLimit)
	}
	if got := libraryCandidates(t, g, "maya", "q=review&limit=10"); len(got.Candidates) != 10 {
		t.Fatalf("limit=10 returned %d", len(got.Candidates))
	}
	if got := libraryCandidates(t, g, "maya", "q=review&limit=5000"); len(got.Candidates) != librarysearch.MaxLimit {
		t.Fatalf("a limit over the bound is clamped to %d, got %d", librarysearch.MaxLimit, len(got.Candidates))
	}
}

func TestLibraryCandidatesRefuseBadInputAndAnonymous(t *testing.T) {
	g := newContentGateway(t)
	for _, q := range []string{"q=x&limit=0", "q=x&limit=-3", "q=x&limit=abc"} {
		if code, _ := g.do("maya", http.MethodGet, "/v1/library/candidates?"+q, "", nil); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}
	resp, err := http.Get(g.srv.URL + "/v1/library/candidates?q=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401", resp.StatusCode)
	}
	// An empty request is a valid query: it shortlists only what was offered.
	if got := libraryCandidates(t, g, "maya", "q="); got.Candidates == nil {
		t.Errorf("an empty answer is [] not null")
	}
}

// A deployment with a larger Library plugs its own ranker behind the seam.
func TestLibraryCandidatesUseThePluggedRanker(t *testing.T) {
	g := newContentGateway(t)
	saveExpert(t, g, "maya", "alpha", "first thing")
	saveExpert(t, g, "maya", "beta", "second thing")
	g.svc.LibraryRanker = nameDescRanker{}
	got := libraryCandidates(t, g, "maya", "q=anything")
	if len(got.Candidates) != 2 || got.Candidates[0].Name != "beta" {
		t.Fatalf("the plugged ranker orders the shortlist: %+v", got.Candidates)
	}
}

// nameDescRanker orders by name, descending, whatever order it is given.
type nameDescRanker struct{}

func (nameDescRanker) Rank(_ string, docs []librarysearch.Doc, limit int) []librarysearch.Doc {
	out := append([]librarysearch.Doc(nil), docs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
