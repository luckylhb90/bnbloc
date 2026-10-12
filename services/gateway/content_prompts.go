package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Prompt packs (docs/prd/2026-10-12-experts-and-teams.md §4): the routes a
// composer needs beside the generic content routes — "Save as prompt" into
// the member's personal pack and a prompt's use count — and the prompt
// fields of content views (a pack's prompts with their use counts, an
// expert's quick prompts resolved to a pack's prompt).

// The member's personal prompt pack: a normal pack they own, created by
// their first "Save as prompt". A product constant (the name a save looks
// for), not deployment data.
const (
	personalPromptPack        = "my-prompts"
	personalPromptPackTitle   = "My prompts"
	personalPromptPackSummary = "Prompts you saved from your rooms."
	// savePromptAttempts bounds the retries of a save that raced another
	// save of the same pack (each retry re-reads the latest version).
	savePromptAttempts = 3
	maxSavePromptBytes = 64 << 10
)

func (s *Service) registerContentPrompts(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/content-components/prompts", s.handleSavePrompt)
	mux.HandleFunc("POST /v1/content-components/{id}/prompts/{promptId}/use", s.handlePromptUse)
}

// errPromptPackRaced: another save moved the personal pack meanwhile.
var errPromptPackRaced = errors.New("prompt pack changed while saving")

// handleSavePrompt appends a prompt to the caller's personal prompt pack
// as a new version, creating the pack (and turning it on for the caller)
// on the first save. Placeholders in the body become variables.
func (s *Service) handleSavePrompt(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.contentCaller(w, r, true)
	if !ok {
		return
	}
	raw, ok := readBody(w, r, maxSavePromptBytes)
	if !ok {
		return
	}
	var req wire.SavePromptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		http.Error(w, "the prompt is empty", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	for attempt := 0; attempt < savePromptAttempts; attempt++ {
		c, promptID, created, err := s.savePrompt(ctx, claims, req)
		var bad badPrompt
		switch {
		case errors.Is(err, errPromptPackRaced):
			continue
		case errors.As(err, &bad):
			http.Error(w, bad.Error(), http.StatusBadRequest)
			return
		case err != nil:
			obs.ServerError(w, "save prompt failed", err)
			http.Error(w, "save prompt failed", http.StatusBadGateway)
			return
		}
		views, err := s.contentViews(ctx, claims, []store.ContentComponent{c}, nil)
		if err != nil || len(views) == 0 {
			if err == nil {
				err = errors.New("saved version missing")
			}
			obs.ServerError(w, "load prompt pack failed", err)
			http.Error(w, "load prompt pack failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, wire.SavePromptReply{Component: views[0], PromptID: promptID, Created: created})
		return
	}
	http.Error(w, "your prompt pack changed while saving: save again", http.StatusConflict)
}

// badPrompt is a save the validator refused (a 400 with its message).
type badPrompt struct{ err error }

func (b badPrompt) Error() string { return b.err.Error() }

// savePrompt writes one save: the next version of the caller's personal
// pack (or its first). It returns errPromptPackRaced when another save
// moved the pack, badPrompt when the result does not validate.
func (s *Service) savePrompt(ctx context.Context, claims auth.Claims, req wire.SavePromptRequest) (store.ContentComponent, string, bool, error) {
	list, err := s.ContentComponents.ListContentComponents(ctx, store.ContentComponentQuery{
		TenantID: claims.Tenant, Owner: claims.Subject, Kind: contentpkg.KindPrompt, Scopes: []store.ContentScope{store.ContentScopePersonal},
	})
	if err != nil {
		return store.ContentComponent{}, "", false, err
	}
	var pack *store.ContentComponent
	for i := range list {
		if list[i].Name == personalPromptPack {
			pack = &list[i]
		}
	}
	m := contentpkg.Manifest{Kind: contentpkg.KindPrompt, Name: personalPromptPack, Version: "1.0.0",
		DisplayName: personalPromptPackTitle, Description: personalPromptPackSummary}
	files := map[string][]byte{}
	if pack != nil {
		v, err := s.ContentComponents.GetContentComponentVersion(ctx, pack.ID, pack.LatestVersion)
		if err != nil {
			return store.ContentComponent{}, "", false, err
		}
		base, err := s.contentPackage(ctx, v)
		if err != nil {
			return store.ContentComponent{}, "", false, err
		}
		m = base.Manifest
		if m.Version, err = semver.BumpPatch(pack.LatestVersion); err != nil {
			return store.ContentComponent{}, "", false, err
		}
		if readme, ok := base.Bundle.File(contentpkg.ReadmeFile); ok {
			files[contentpkg.ReadmeFile] = readme
		}
	}
	p := contentpkg.NormalizePrompts([]contentpkg.Prompt{{Title: req.Title, Body: req.Body, Tags: req.Tags}})[0]
	p.ID = contentpkg.UniquePromptID(p.Title, m.Prompts)
	m.Prompts = append(m.Prompts, p)
	pkg, bundle, err := contentpkg.Pack(m, files, contentOptions())
	if err != nil {
		return store.ContentComponent{}, "", false, badPrompt{err}
	}
	key, err := s.storeBundle(ctx, pkg, bundle)
	if err != nil {
		return store.ContentComponent{}, "", false, err
	}
	now := s.nowTime().UnixMilli()
	ver, err := contentVersionRow(pkg, key, len(bundle), claims.Subject, now)
	if err != nil {
		return store.ContentComponent{}, "", false, err
	}
	if pack != nil {
		next := *pack
		next.DisplayName, next.Description, next.Category, next.UpdatedAtMs = m.DisplayName, m.Description, m.Category, now
		switch err := s.ContentComponents.AddContentComponentVersion(ctx, next, pack.LatestVersion, ver); {
		case errors.Is(err, store.ErrContentVersionConflict):
			return store.ContentComponent{}, "", false, errPromptPackRaced
		case err != nil:
			return store.ContentComponent{}, "", false, err
		}
		next.LatestVersion = ver.Version
		return next, p.ID, false, nil
	}
	c := store.ContentComponent{
		ID: ids.New("cc_"), Kind: contentpkg.KindPrompt, Name: personalPromptPack, TenantID: claims.Tenant, Owner: claims.Subject,
		Scope: store.ContentScopePersonal, Status: store.ContentStatusActive,
		DisplayName: m.DisplayName, Description: m.Description, CreatedAtMs: now, UpdatedAtMs: now,
	}
	switch err := s.ContentComponents.CreateContentComponent(ctx, c, ver); {
	case errors.Is(err, store.ErrContentExists):
		return store.ContentComponent{}, "", false, errPromptPackRaced
	case err != nil:
		return store.ContentComponent{}, "", false, err
	}
	c.LatestVersion = ver.Version
	// The personal pack is on for its owner from the first save: their
	// saved prompts show in their composer at once.
	if err := s.ContentComponents.PutContentUse(ctx, store.ContentUse{TenantID: claims.Tenant, ScopeKind: store.ContentUseMember, ScopeID: claims.Subject,
		ComponentID: c.ID, Kind: c.Kind, CreatedBy: claims.Subject, CreatedAtMs: now}); err != nil {
		return store.ContentComponent{}, "", false, err
	}
	return c, p.ID, true, nil
}

// handlePromptUse counts one insertion of a pack's prompt (the composer
// calls it once per insertion). The caller must be able to read the pack:
// visible to them, or turned on in a room of theirs (?room=); the prompt
// must be in its latest version or the one named (?version=, a room pin).
func (s *Service) handlePromptUse(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.contentCaller(w, r, false)
	if !ok {
		return
	}
	if !s.rateAllow(claims.Subject) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	c, err := s.readableContent(ctx, claims, r.PathValue("id"), q.Get("room"))
	if err == nil && c.Kind != contentpkg.KindPrompt {
		err = store.ErrContentNotFound
	}
	if err != nil {
		contentStoreErr(w, "load prompt pack failed", err)
		return
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, firstNonEmpty(q.Get("version"), c.LatestVersion))
	if err != nil {
		contentStoreErr(w, "load prompt pack version failed", err)
		return
	}
	m, err := contentuse.Manifest(v)
	if err != nil {
		obs.ServerError(w, "decode prompt pack failed", err)
		http.Error(w, "decode prompt pack failed", http.StatusInternalServerError)
		return
	}
	promptID := r.PathValue("promptId")
	if _, ok := m.FindPrompt(promptID); !ok {
		http.Error(w, "prompt not found", http.StatusNotFound)
		return
	}
	if err := s.ContentComponents.RecordContentPromptUse(ctx, c.ID, promptID, s.nowTime().UnixMilli()); err != nil {
		contentStoreErr(w, "record prompt use failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// promptFields fills a view's prompt pack prompts and an expert's quick
// prompt references from the version's manifest.
func promptFields(m contentpkg.Manifest, out *wire.ContentComponent) {
	for _, p := range m.Prompts {
		out.Prompts = append(out.Prompts, contentuse.WirePrompt(p))
	}
	for i, q := range m.QuickPrompts {
		if i < len(out.QuickPrompts) {
			out.QuickPrompts[i].Ref = q.Ref
		}
	}
}

// enrichPromptViews adds what a view's manifest alone cannot say: each
// prompt's use count, and each expert quick prompt reference resolved for
// the viewer (in roomID when given) to a pack's prompt. A reference no
// usable pack satisfies stays unresolved (the composer says what it needs).
func (s *Service) enrichPromptViews(ctx context.Context, claims auth.Claims, roomID string, views []wire.ContentComponent) error {
	var packs []string
	for _, v := range views {
		if len(v.Prompts) > 0 {
			packs = append(packs, v.ID)
		}
	}
	if len(packs) > 0 {
		counts, err := s.ContentComponents.ContentPromptUses(ctx, packs)
		if err != nil {
			return fmt.Errorf("prompt use counts: %w", err)
		}
		for i := range views {
			for j := range views[i].Prompts {
				views[i].Prompts[j].Uses = counts[views[i].ID][views[i].Prompts[j].ID]
			}
		}
	}
	viewer := viewerOf(claims)
	for i := range views {
		for j := range views[i].QuickPrompts {
			q := &views[i].QuickPrompts[j]
			if q.Ref == "" || q.Resolved != nil {
				continue
			}
			ref, err := contentuse.ResolvePromptRef(ctx, s.ContentComponents, viewer, roomID, views[i].Requires.Prompts, q.Ref)
			if err != nil {
				return fmt.Errorf("resolve quick prompt %s: %w", q.Ref, err)
			}
			if ref != nil {
				q.Resolved = ref
				if q.Title == "" {
					q.Title = ref.Prompt.Title
				}
			}
		}
	}
	return nil
}
