package gateway

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/librarysearch"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// GET /v1/library/candidates?q=<request>&limit=N (docs/prd/2026-10-25-suggest-what-to-use.md):
// the first stage of a suggestion. The member's Library is what they may see (the one
// visibility function, contentuse.Visible, for experts and skill packs; the member
// catalog for MCP tools they have not added), ranked against the request by
// librarysearch and cut to a shortlist, so the second stage (the decision model, one
// choice question) costs the same at any size of Library. The request is the query:
// it is not stored, and the audit records the count and the time, never the text.
//
// A component an administrator put forward (organization and platform scope) always
// qualifies for the shortlist's free room: what a request shares no word with is the
// decision model's to read.
func (s *Service) handleLibraryCandidates(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	claims := actor.Claims
	request := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(request) > wire.MaxLibraryRequestRunes {
		request = string([]rune(request)[:wire.MaxLibraryRequestRunes])
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpx.Error(w, "limit must be a positive number", http.StatusBadRequest)
			return
		}
		limit = n
	}
	start := time.Now()
	docs, byKey, err := s.libraryDocs(r, claims)
	if err != nil {
		obs.ServerError(w, "list library candidates failed", err)
		httpx.Error(w, "list library candidates failed", http.StatusInternalServerError)
		return
	}
	out := wire.LibraryCandidates{Candidates: []wire.LibraryCandidate{}, Total: len(docs)}
	for _, d := range s.libraryRanker().Rank(request, docs, limit) {
		out.Candidates = append(out.Candidates, byKey[d.Key])
	}
	slog.Debug("bnbloc-gateway: library candidates", "total", out.Total, "returned", len(out.Candidates), "ms", time.Since(start).Milliseconds())
	httpx.WriteJSON(w, http.StatusOK, out)
}

// libraryRanker is the ranker behind the seam: the lexical default; a deployment with a
// larger Library sets Service.LibraryRanker.
func (s *Service) libraryRanker() librarysearch.Ranker {
	if s.LibraryRanker != nil {
		return s.LibraryRanker
	}
	return librarysearch.Lexical{}
}

// libraryDocs reads what the member may use as ranker input and the wire form to answer
// with, both keyed by one candidate key.
func (s *Service) libraryDocs(r *http.Request, claims auth.Claims) ([]librarysearch.Doc, map[string]wire.LibraryCandidate, error) {
	ctx := r.Context()
	var docs []librarysearch.Doc
	byKey := map[string]wire.LibraryCandidate{}
	if s.ContentComponents != nil {
		viewer := viewerOf(claims)
		for _, kind := range []string{contentpkg.KindExpert, contentpkg.KindSkill} {
			list, err := contentuse.Visible(ctx, s.ContentComponents, viewer, kind)
			if err != nil {
				return nil, nil, err
			}
			for _, c := range list {
				if c.Status != store.ContentStatusActive {
					continue
				}
				v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, c.LatestVersion)
				if errors.Is(err, store.ErrContentNotFound) {
					continue
				}
				if err != nil {
					return nil, nil, err
				}
				m, err := contentuse.Manifest(v)
				if err != nil {
					return nil, nil, err
				}
				cand := wire.LibraryCandidate{
					ID: c.ID, Kind: shortKind(c.Kind), Name: c.Name, Label: firstNonEmpty(m.DisplayName, c.DisplayName, c.Name),
					Description: trimRunes(firstNonEmpty(m.Description, c.Description), wire.MaxLibraryDescriptionRunes), Tags: m.Tags, Version: c.LatestVersion,
				}
				for _, ref := range m.Requires.Skills {
					cand.Wears = append(cand.Wears, strings.SplitN(ref, "@", 2)[0])
				}
				key := cand.Kind + "/" + c.ID
				byKey[key] = cand
				docs = append(docs, librarysearch.Doc{Key: key, Name: c.Name, Label: cand.Label, Tags: m.Tags, Description: firstNonEmpty(m.Description, c.Description),
					Offered: c.Scope == store.ContentScopeOrg || c.Scope == store.ContentScopePlatform})
			}
		}
	}
	view, err := s.loadMemberComponents(ctx, claims)
	if err != nil {
		return nil, nil, err
	}
	for _, rec := range view.list {
		vis, listed := view.listed(rec)
		if !listed || !isMCP(rec) || vis.Installed {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil {
			return nil, nil, err
		}
		key := "mcp/" + rec.Name
		byKey[key] = wire.LibraryCandidate{Kind: "mcp", Name: rec.Name, Label: rec.Name, Description: trimRunes(cfg.Description, wire.MaxLibraryDescriptionRunes)}
		docs = append(docs, librarysearch.Doc{Key: key, Name: rec.Name, Label: rec.Name, Description: cfg.Description, Offered: true})
	}
	return docs, byKey, nil
}

func trimRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
