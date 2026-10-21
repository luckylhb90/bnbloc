package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A work version's stored cover (docs/prd/2026-10-19-work-covers.md §2.3):
// a published work is immutable, so a version published without a cover
// gets one beside its bundle the first time someone opens it, as a room
// version gets its thumbnail (services/room/artifact_files.go):
//
//	PUT  /v1/content-components/{id}/versions/{version}/cover?source=render  a reader's viewer's picture
//	POST /v1/content-components/{id}/versions/{version}/cover/ai             the author's AI cover
//
// The cover route answers the bundle's own cover first, then the stored
// one (workCoverOf). A render is kept only when the version has neither;
// the AI cover (store.ContentCoverAI) replaces a render, and is drawn only
// for a version with no cover of its own and no AI cover yet.

// workVersionRef is the work version a cover or file route names, with its
// package.
type workVersionRef struct {
	actor contentActor
	c     store.ContentComponent
	v     store.ContentComponentVersion
	pkg   *contentpkg.Package
}

// workVersion loads the work version a route names
// (/v1/content-components/{id}/versions/{version}…): one the caller may
// read (readableContent), or with own one they author (ownContent). It
// answers a failure itself (false).
func (s *Service) workVersion(w http.ResponseWriter, r *http.Request, caller auth.Claims, own bool) (workVersionRef, bool) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return workVersionRef{}, false
	}
	ctx, id := r.Context(), r.PathValue("id")
	var c store.ContentComponent
	var err error
	if own {
		c, err = s.ownContent(ctx, actor.Claims, id)
	} else {
		c, err = s.readableContent(ctx, actor.Claims, id, actor.Room)
	}
	if err == nil && c.Kind != contentpkg.KindWork {
		err = store.ErrContentNotFound
	}
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return workVersionRef{}, false
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, r.PathValue("version"))
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return workVersionRef{}, false
	}
	pkg, err := s.contentPackage(ctx, v)
	if err != nil {
		obs.ServerError(w, "load content bundle failed", err)
		httpx.Error(w, "load content bundle failed", http.StatusBadGateway)
		return workVersionRef{}, false
	}
	return workVersionRef{actor: actor, c: c, v: v, pkg: pkg}, true
}

// bundleCover is the version's own cover (contentpkg.WorkCover, its
// bundle's cover order); a refused cover file is none.
func bundleCover(ref workVersionRef) (tarbundle.Cover, bool) {
	cover, found, err := contentpkg.WorkCover(ref.pkg)
	return cover, found && err == nil
}

// coverIDs are the components a version's stored cover is kept under, in
// order: for an organization publication or a shared copy, first the
// author's component it was made from (the same version is the same
// bundle, so the author's card and every copy show one cover), then its
// own (the author's is gone).
func coverIDs(c store.ContentComponent) []string {
	if c.SourceID == "" {
		return []string{c.ID}
	}
	return []string{c.SourceID, c.ID}
}

// storedCover is the cover stored for the version (coverIDs).
func (s *Service) storedCover(ctx context.Context, ref workVersionRef) (store.ContentVersionCover, bool, error) {
	for _, id := range coverIDs(ref.c) {
		if sc, found, err := s.ContentComponents.GetContentVersionCover(ctx, id, ref.v.Version); err != nil || found {
			return sc, found, err
		}
	}
	return store.ContentVersionCover{}, false, nil
}

// workCoverOf is the cover the version shows: the bundle's own, else the
// stored one (its bytes from the blob store); found=false for none.
func (s *Service) workCoverOf(ctx context.Context, ref workVersionRef) (tarbundle.Cover, bool, error) {
	if cover, ok := bundleCover(ref); ok {
		return cover, true, nil
	}
	sc, found, err := s.storedCover(ctx, ref)
	if err != nil || !found {
		return tarbundle.Cover{}, false, err
	}
	data, err := s.readArtifactBlob(ctx, sc.BlobKey, wire.MaxWorkCoverBytes)
	if err != nil {
		return tarbundle.Cover{}, false, err
	}
	return tarbundle.Cover{MimeType: sc.MimeType, Data: data}, true, nil
}

// handlePutWorkCover stores a reader's picture of a work version as its
// cover (source=render): a PNG, WebP or JPEG body of the type its
// Content-Type says, within wire.MaxWorkCoverBytes (tarbundle.CoverImage).
// It is kept only when the version has no cover; any member who may read
// the work may send one.
func (s *Service) handlePutWorkCover(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	ref, ok := s.workVersion(w, r, caller, false)
	if !ok {
		return
	}
	if r.URL.Query().Get("source") != store.ContentCoverRender {
		httpx.Error(w, "source must be render: an AI cover is drawn by POST …/cover/ai", http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, wire.MaxWorkCoverBytes+1))
	if err != nil {
		httpx.Error(w, "read the cover failed", http.StatusBadRequest)
		return
	}
	cover, err := tarbundle.CoverImage(raw)
	switch {
	case errors.Is(err, tarbundle.ErrCoverTooLarge):
		httpx.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		httpx.Error(w, err.Error(), http.StatusUnsupportedMediaType)
		return
	case cover.MimeType != store.MediaType(r.Header.Get("Content-Type")):
		httpx.Error(w, "the cover is not the "+cover.MimeType+" its Content-Type says", http.StatusUnsupportedMediaType)
		return
	}
	if _, ok := bundleCover(ref); ok {
		httpx.WriteJSON(w, http.StatusOK, wire.WorkCoverStored{Stored: false})
		return
	}
	if _, found, err := s.storedCover(r.Context(), ref); err != nil {
		obs.ServerError(w, "load the stored cover failed", err)
		httpx.Error(w, "load the stored cover failed", http.StatusInternalServerError)
		return
	} else if found {
		httpx.WriteJSON(w, http.StatusOK, wire.WorkCoverStored{Stored: false})
		return
	}
	s.keepWorkCover(w, r.Context(), ref, cover, store.ContentCoverRender)
}

// handleDrawStoredWorkCover draws an AI cover for the caller's own work
// version (drawWorkCover, from its manifest's title, description and type)
// and stores it (source ai, which replaces a render): the detail's cover
// for a work no viewer draws, when its author opens it. A version with a
// cover of its own or an AI cover already is a 409, drawn for nothing.
func (s *Service) handleDrawStoredWorkCover(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	ref, ok := s.workVersion(w, r, caller, true)
	if !ok {
		return
	}
	ctx, claims := r.Context(), ref.actor.Claims
	if _, ok := bundleCover(ref); ok {
		httpx.Error(w, "this work has a cover of its own", http.StatusConflict)
		return
	}
	if sc, found, err := s.storedCover(ctx, ref); err != nil {
		obs.ServerError(w, "load the stored cover failed", err)
		httpx.Error(w, "load the stored cover failed", http.StatusInternalServerError)
		return
	} else if found && sc.Rank >= store.ContentCoverRank(store.ContentCoverAI) {
		httpx.Error(w, "this work already has an AI cover", http.StatusConflict)
		return
	}
	if !s.assistLimiter.Get(assistRate, assistBurst, s.Peers.Size).Allow(claims.Tenant + "/" + claims.Subject) {
		httpx.Error(w, "too many requests: try again in a minute", http.StatusTooManyRequests)
		return
	}
	m := ref.pkg.Manifest
	drawn, _, err := s.drawWorkCover(ctx, claims, ownAuthOf(r, ref.actor), firstNonEmpty(m.DisplayName, m.Name), m.Description, m.Type)
	if !writeCoverErr(w, err) {
		return
	}
	cover, err := tarbundle.CoverImage(drawn)
	if !writeCoverErr(w, err) {
		return
	}
	s.keepWorkCover(w, ctx, ref, cover, store.ContentCoverAI)
}

// keepWorkCover puts cover into the blob store and stores it as the
// version's cover from source (under the first of coverIDs whose version
// exists), answering whether it was kept.
func (s *Service) keepWorkCover(w http.ResponseWriter, ctx context.Context, ref workVersionRef, cover tarbundle.Cover, source string) {
	if strings.TrimSpace(s.Backends.Realtime) == "" {
		httpx.Unavailable(w, "artifact blob store")
		return
	}
	blob, err := s.blobs().PutBlob(ctx, bytes.NewReader(cover.Data))
	if err != nil {
		obs.ServerError(w, "store the cover failed", err)
		httpx.Error(w, "store the cover failed", http.StatusBadGateway)
		return
	}
	kept := false
	for _, id := range coverIDs(ref.c) {
		_, kept, err = s.ContentComponents.SetContentVersionCover(ctx, store.ContentVersionCover{
			ComponentID: id, Version: ref.v.Version, BlobKey: blob.Key, MimeType: cover.MimeType,
			Rank: store.ContentCoverRank(source), CreatedBy: ref.actor.Claims.Subject, CreatedAtMs: s.nowTime().UnixMilli(),
		})
		if !errors.Is(err, store.ErrContentNotFound) {
			break
		}
	}
	if err != nil {
		contentStoreErr(w, "record the cover failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.WorkCoverStored{Stored: kept})
}
