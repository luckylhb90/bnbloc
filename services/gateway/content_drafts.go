package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Quick tries of unsaved drafts (docs/prd/2026-10-12-experts-and-teams.md,
// "Runtime gap closure" 4). The room tools' components try stages the expert or
// skill pack an agent drafted in its workspace here, then dispatches a
// sample task to another agent wearing it (wire.DispatchRequest.Draft).
// Staging is a request on behalf of the running job's requester (the
// ?job=&room= grant of content_requester.go): the package is validated
// like a save, its bundle goes to the blob store, and a content_drafts row
// scopes it to that job, its room and an expiry. No content component is
// created, so no Library or listing ever shows a draft.

// handleStageContentDraft serves POST /v1/content-drafts?job=&room=.
func (s *Service) handleStageContentDraft(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	if actor.Job == nil {
		httpx.Error(w, "a draft is staged by the bridge running a job (?job=&room=)", http.StatusBadRequest)
		return
	}
	opts, ok := s.saveContentOptions(w, r, actor.Claims)
	if !ok {
		return
	}
	if !isArchiveBody(r) {
		httpx.Error(w, "send the draft as a package archive (application/x-tar)", http.StatusUnsupportedMediaType)
		return
	}
	raw, ok := readBody(w, r, tarbundle.ContentLimits.MaxArchiveBytes())
	if !ok {
		return
	}
	pkg, bundle, err := importArchive(raw, opts)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m := pkg.Manifest
	switch {
	case m.Kind != contentpkg.KindExpert && m.Kind != contentpkg.KindSkill:
		httpx.Error(w, "only an expert or a skill pack can be tried: an agent wears it", http.StatusBadRequest)
		return
	case m.Team != nil:
		httpx.Error(w, "a team cannot be tried as a draft: try each member's expert, then save the team", http.StatusBadRequest)
		return
	}
	key, err := s.storeBundle(r.Context(), pkg, bundle)
	if err != nil {
		obs.ServerError(w, "store draft bundle failed", err)
		httpx.Error(w, "store draft bundle failed", http.StatusBadGateway)
		return
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		obs.ServerError(w, "encode draft manifest failed", err)
		httpx.Error(w, "encode draft manifest failed", http.StatusInternalServerError)
		return
	}
	job := actor.Job
	now := s.nowTime().UnixMilli()
	d := store.ContentDraft{
		ID: ids.New(store.ContentDraftIDPrefix), TenantID: job.TenantID, RoomID: job.RoomID, JobID: job.JobID,
		// The agent that wrote the files is its owner's: someone else's
		// script-bearing draft forces the sandbox on the agent wearing it.
		Requester: job.RequesterUserID, Author: job.OwnerUserID,
		Kind: m.Kind, Name: m.Name, DisplayName: m.DisplayName, Version: m.Version, ManifestJSON: manifest,
		BundleBlobKey: key, SHA256: pkg.SHA256, SizeBytes: int64(len(bundle)),
		CreatedAtMs: now, ExpiresAtMs: now + store.ContentDraftTTL.Milliseconds(),
	}
	if err := s.ContentComponents.PutContentDraft(tenant.NewContext(r.Context(), job.TenantID), d); err != nil {
		obs.ServerError(w, "stage content draft failed", err)
		httpx.Error(w, "stage content draft failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, wire.ContentDraft{ID: d.ID, Kind: shortKind(m.Kind), Name: m.Name, DisplayName: m.DisplayName, Version: m.Version, ExpiresAtMs: d.ExpiresAtMs})
}
