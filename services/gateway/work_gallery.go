package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentshare"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The works gallery's server side (docs/prd/2026-10-13-app-gallery.md,
// docs/prd/2026-10-13-works-gallery-and-make-similar.md): any deliverable
// is published as a content component of kind "work" (contentpkg), so
// scopes, org approval, platform signing, visibility (contentuse.Visible)
// and the Library apply to it unchanged. These routes add what only a work
// has:
//
//	POST /v1/rooms/{roomId}/artifacts/{artifactId}/publish-work  a deliverable's current version as a work of the caller's
//	GET  /v1/content-components/{id}/versions/{version}/cover    a work's cover image (its own, else its stored one)
//	PUT|DELETE /v1/content-components/{id}/favorite              the caller's star
//	POST /v1/content-components/{id}/use-app                     an app's copy as a new site in a room the caller owns
//
// A work never holds data: publishing snapshots the deliverable's files
// (the room service's version, read as the member), and an app's copy is
// a new site artifact whose data space, keyed by its new id, starts empty.

// Bounds of the gallery's requests.
const (
	// maxWorkRequestBytes bounds a publish request: its fields and a
	// chosen cover, a data URL (base64 is 4/3 of the bytes).
	maxWorkRequestBytes = 64<<10 + wire.MaxWorkCoverBytes*4/3 + 64
	// appCopyTimeout bounds a copy's room calls (a site snapshot upload of
	// up to tarbundle.SiteLimits).
	appCopyTimeout = 2 * time.Minute
)

// handlePublishWork snapshots a deliverable's current version (a site's
// files, or its one file, with a cover and README, never its data) as a
// work component of the caller's: a new one, or the next version of one of
// theirs (componentId). Only an owner of the room's deliverables publishes
// one: a member who may chat in it (membership.Member.MayChat, the app
// data owner rule). With ?job= the bridge running the job publishes for
// its requester (jobGrant, the content routes' admission; components save
// with artifactId).
func (s *Service) handlePublishWork(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	ctx := r.Context()
	roomID, artifactID := r.PathValue("roomId"), r.PathValue("artifactId")
	actor, ok := s.deliverableOwner(w, r, caller, roomID)
	if !ok {
		return
	}
	raw, ok := readBody(w, r, maxWorkRequestBytes)
	if !ok {
		return
	}
	var in wire.WorkPublishRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	d, err := s.currentDeliverable(ctx, actor.Claims, roomID, artifactID)
	if !writeDeliverableErr(w, err) {
		return
	}
	if d.Snapshot.Cover, err = s.workCover(ctx, actor.Claims, roomID, d, in.Cover); errors.Is(err, errBadCover) {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	} else if err != nil {
		obs.ServerError(w, "load the work's cover failed", err)
		httpx.Error(w, "load the work's cover failed", http.StatusBadGateway)
		return
	}
	art := d.Artifact
	m := contentpkg.Manifest{
		Name: strings.TrimSpace(in.Name), Version: "1.0.0",
		DisplayName: firstNonEmpty(in.DisplayName, workTitle(art)), Description: strings.TrimSpace(in.Description),
		Category: strings.TrimSpace(in.Category), Tags: in.Tags,
	}
	var existing *store.ContentComponent
	if id := strings.TrimSpace(in.ComponentID); id != "" {
		c, err := s.ownContent(ctx, actor.Claims, id)
		if err != nil {
			contentStoreErr(w, "load content component failed", err)
			return
		}
		if c.Kind != contentpkg.KindWork {
			httpx.Error(w, fmt.Sprintf("%s is a %s, not a work", c.Name, shortKind(c.Kind)), http.StatusBadRequest)
			return
		}
		if m.Version, err = nextContentVersion(c, ""); err != nil {
			httpx.Error(w, err.Error(), http.StatusConflict)
			return
		}
		latest, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, c.LatestVersion)
		if err != nil {
			contentStoreErr(w, "load content version failed", err)
			return
		}
		prev, err := contentuse.Manifest(latest)
		if err != nil {
			obs.ServerError(w, "decode content manifest failed", err)
			httpx.Error(w, "decode content manifest failed", http.StatusInternalServerError)
			return
		}
		// The next version keeps what the request leaves out.
		// A title that was only the deliverable's own (a path, for an
		// agent's file) was never chosen: the default applies again.
		kept := prev.DisplayName
		if kept == art.Title {
			kept = ""
		}
		m.DisplayName = firstNonEmpty(in.DisplayName, kept, workTitle(art))
		m.Description = firstNonEmpty(in.Description, prev.Description)
		m.Category = firstNonEmpty(in.Category, prev.Category)
		if len(m.Tags) == 0 {
			m.Tags = prev.Tags
		}
		// So does the recipe's request text: an author's rewording is kept
		// (the rest of the recipe is captured again).
		if in.Recipe == nil && strings.TrimSpace(in.RecipePrompt) == "" && prev.Recipe != nil {
			in.RecipePrompt = prev.Recipe.Prompt
		}
		m.Name, existing = c.Name, &c
	}
	if m.Name == "" {
		m.Name = firstNonEmpty(contentpkg.NameFrom(m.DisplayName), contentpkg.NameFrom(art.FileName), "work")
	}
	// The recipe: captured from the producing job, or the author's edit
	// of it, or none.
	if m.Recipe, d.Snapshot.References, err = s.publishedRecipe(ctx, actor.Claims, roomID, d.Version, in); !writeRecipeErr(w, err) {
		return
	}
	pkg, bundle, err := contentpkg.WorkFromDeliverable(d.Source, d.Snapshot, m)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !jobSaveAllowed(w, actor, pkg) {
		return
	}
	if existing != nil {
		s.addContentVersion(w, r, actor, *existing, pkg, bundle)
		return
	}
	s.createContent(w, r, actor, pkg, bundle)
}

// deliverableOwner admits a request acting on a room's deliverable as an
// owner of them: the caller, or with ?job= the job's requester (jobGrant),
// who may chat in the room (the app data owner rule). It answers the
// refusal.
func (s *Service) deliverableOwner(w http.ResponseWriter, r *http.Request, caller auth.Claims, roomID string) (contentActor, bool) {
	claims, ok := s.roomClaims(caller, roomID)
	if !ok {
		httpx.Error(w, "room scope not authorized", http.StatusForbidden)
		return contentActor{}, false
	}
	actor := contentActor{Claims: claims, Room: roomID}
	if job := strings.TrimSpace(r.URL.Query().Get("job")); job != "" {
		var err error
		if actor, err = s.jobGrant(r.Context(), caller, claims, roomID, job); err != nil {
			writeGrantErr(w, err)
			return contentActor{}, false
		}
	}
	if m, ok := s.roomMember(actor.Subject, roomID); !ok || !m.MayChat() {
		httpx.Error(w, "only the room's members who may post in it (its deliverables' owners) publish its works", http.StatusForbidden)
		return contentActor{}, false
	}
	return actor, true
}

// deliverable is a deliverable's current version as a work snapshots it.
type deliverable struct {
	Artifact wire.Artifact
	Version  wire.ArtifactVersion
	Source   contentpkg.WorkDeliverable
	Snapshot contentpkg.WorkSnapshot
}

// errDeliverableTooLarge: a single file over a work's cap.
var errDeliverableTooLarge = fmt.Errorf("the deliverable is larger than a work holds (%d bytes)", tarbundle.SiteLimits.MaxBytes)

// writeDeliverableErr answers a currentDeliverable failure and reports
// whether there was none.
func writeDeliverableErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, errSiteNotFound):
		httpx.Error(w, "no published deliverable with that id in this room", http.StatusNotFound)
	case errors.Is(err, errDeliverableTooLarge):
		httpx.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	default:
		obs.ServerError(w, "load deliverable failed", err)
		httpx.Error(w, "load deliverable failed", http.StatusBadGateway)
	}
	return false
}

// currentDeliverable reads a deliverable's current version as the member
// in claims (the room service's own membership and artifact checks
// decide): the artifact, the version and its snapshot (a site's bundle, or
// the file's bytes). errSiteNotFound when it is not a published
// deliverable of the room the member reads.
func (s *Service) currentDeliverable(ctx context.Context, claims auth.Claims, roomID, artifactID string) (deliverable, error) {
	art, v, err := s.deliverableVersion(ctx, claims, roomID, artifactID)
	if err != nil {
		return deliverable{}, err
	}
	d := deliverable{Artifact: art, Version: v, Source: contentpkg.WorkDeliverable{Artifact: art}}
	if art.Kind == store.ArtifactKindSite {
		sv, err := s.memberSiteVersion(ctx, claims.Subject, claims.Tenant, siteTicketScope(roomID, art.ArtifactID, art.CurrentVersion))
		if err != nil {
			return deliverable{}, err
		}
		if d.Snapshot.Site, err = s.siteBundle(ctx, sv); err != nil {
			return deliverable{}, err
		}
		return d, nil
	}
	path := "/v1/rooms/" + url.PathEscape(roomID) + "/artifacts/" + url.PathEscape(artifactID) + "/versions/" + strconv.Itoa(art.CurrentVersion) + "/content"
	data, err := s.roomBackendBytes(ctx, path, claims, int64(tarbundle.SiteLimits.MaxBytes))
	if err != nil {
		return deliverable{}, err
	}
	d.Snapshot.Name, d.Snapshot.Data = store.ArtifactDownloadName(art.Kind, art.FileName, art.Title), data
	return d, nil
}

// deliverableVersion reads a deliverable and its current version's
// metadata (its producing job and request) as the member in claims;
// errSiteNotFound when it is not a published deliverable of the room the
// member reads.
func (s *Service) deliverableVersion(ctx context.Context, claims auth.Claims, roomID, artifactID string) (wire.Artifact, wire.ArtifactVersion, error) {
	var detail wire.ArtifactDetail
	err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/artifacts/"+url.PathEscape(artifactID), "", nil, claims.Subject, claims.Tenant, &detail)
	var rbe *roomBackendError
	switch {
	case errors.As(err, &rbe) && rbe.Status < http.StatusInternalServerError:
		return wire.Artifact{}, wire.ArtifactVersion{}, errSiteNotFound
	case err != nil:
		return wire.Artifact{}, wire.ArtifactVersion{}, err
	}
	art := detail.Artifact
	if art.CurrentVersion < 1 {
		return wire.Artifact{}, wire.ArtifactVersion{}, errSiteNotFound
	}
	for _, v := range detail.Versions {
		if v.Version == art.CurrentVersion {
			return art, v, nil
		}
	}
	return art, wire.ArtifactVersion{ArtifactID: art.ArtifactID, Version: art.CurrentVersion}, nil
}

// errBadCover: the cover the request carries is not a cover image.
var errBadCover = errors.New("the cover is not a PNG, WebP or JPEG image within the cover cap")

// workCover is the cover a deliverable publishes with
// (docs/prd/2026-10-19-work-covers.md §2), under a site's own cover file
// (WorkFromDeliverable keeps that): the one the person chose (chosen, a
// data URL; errBadCover when it is none), the producing turn's, an image's
// own image fitted to the cover cap, the room's thumbnail of the version;
// nil for none (the generated cover). An image that already fits is its
// own cover (WorkCover), so it is not copied.
func (s *Service) workCover(ctx context.Context, claims auth.Claims, roomID string, d deliverable, chosen string) ([]byte, error) {
	if chosen = strings.TrimSpace(chosen); chosen != "" {
		data, _, err := decodeDataURLImage(chosen)
		if err != nil {
			return nil, errBadCover
		}
		if _, err := tarbundle.CoverFile(data); err != nil {
			return nil, errBadCover
		}
		return data, nil
	}
	if d.Snapshot.Site == nil {
		if cover, err := s.turnCover(ctx, claims, roomID, d); err != nil || cover != nil {
			return cover, err
		}
		if d.Artifact.Kind == store.ArtifactKindImage {
			if _, err := tarbundle.CoverFile(d.Snapshot.Data); err == nil {
				return nil, nil
			}
			if cover, err := tarbundle.FitCover(d.Snapshot.Data); err == nil {
				return cover, nil
			}
		}
	}
	return s.versionThumbnail(ctx, claims, roomID, d), nil
}

// versionThumbnail is the thumbnail the room stored for the deliverable's
// version (a viewer's first page of a deck, a PDF, a document), nil when
// it has none or it cannot be read: a cover is never worth failing a
// publish over.
func (s *Service) versionThumbnail(ctx context.Context, claims auth.Claims, roomID string, d deliverable) []byte {
	path := "/v1/rooms/" + url.PathEscape(roomID) + "/artifacts/" + url.PathEscape(d.Artifact.ArtifactID) + "/versions/" + strconv.Itoa(d.Version.Version) + "/thumbnail"
	data, err := s.roomBackendBytes(ctx, path, claims, wire.MaxWorkCoverBytes)
	if err != nil {
		return nil
	}
	if _, err := tarbundle.CoverFile(data); err != nil {
		return nil
	}
	return data
}

// turnCover is the cover a single-file deliverable's producing turn saved
// beside it (a video's frame, the works PRD's cover order): an image
// deliverable of the room named as a cover file (tarbundle.IsCoverFile)
// with a version produced by the same request, whose bytes are a cover
// (tarbundle.CoverFile). nil when there is none.
func (s *Service) turnCover(ctx context.Context, claims auth.Claims, roomID string, d deliverable) ([]byte, error) {
	if d.Version.SourceSeq <= 0 {
		return nil, nil
	}
	q := url.Values{"sourceSeq": {strconv.FormatInt(d.Version.SourceSeq, 10)}, "mime": {"image/*"}}
	var list wire.ArtifactList
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/artifacts?"+q.Encode(), "", nil, claims.Subject, claims.Tenant, &list); err != nil {
		return nil, err
	}
	for _, a := range list.Artifacts {
		if a.ArtifactID == d.Artifact.ArtifactID || !tarbundle.IsCoverFile(firstNonEmpty(a.FileName, a.Title)) {
			continue
		}
		path := "/v1/rooms/" + url.PathEscape(roomID) + "/artifacts/" + url.PathEscape(a.ArtifactID) + "/versions/" + strconv.Itoa(a.CurrentVersion) + "/content"
		data, err := s.roomBackendBytes(ctx, path, claims, wire.MaxWorkCoverBytes)
		if errors.Is(err, errDeliverableTooLarge) || errors.Is(err, errSiteNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, err := tarbundle.CoverFile(data); err == nil {
			return data, nil
		}
	}
	return nil, nil
}

// roomBackendBytes reads a room service answer's bytes as the member in
// claims, at most max (errDeliverableTooLarge beyond); a refusal is
// errSiteNotFound.
func (s *Service) roomBackendBytes(ctx context.Context, path string, claims auth.Claims, max int64) ([]byte, error) {
	if s.Backends.Room == "" {
		return nil, fmt.Errorf("room backend not configured")
	}
	resp, err := callBackendTyped(ctx, backendStreamHTTP, http.MethodGet, strings.TrimRight(s.Backends.Room, "/")+path, "", nil, claims.Subject, claims.Tenant)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		return nil, upstreamStatusError(resp)
	case resp.StatusCode != http.StatusOK:
		return nil, errSiteNotFound
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errDeliverableTooLarge
	}
	return data, nil
}

// workTypeOf is a work version's type (its manifest's); "" for another
// kind or an unreadable manifest.
func workTypeOf(c store.ContentComponent, v store.ContentComponentVersion) string {
	if c.Kind != contentpkg.KindWork {
		return ""
	}
	m, err := contentuse.Manifest(v)
	if err != nil {
		return ""
	}
	return m.Type
}

// handleWorkCover serves a work version's cover image to a caller who may
// read the component (readableContent), as the site cover route does
// (writeCover): the bundle's own (contentpkg.WorkCover, the cover order),
// else the stored one (work_version_cover.go). 404 when the work has
// none, so a card falls back to its generated cover.
func (s *Service) handleWorkCover(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	ref, ok := s.workVersion(w, r, caller, false)
	if !ok {
		return
	}
	cover, found, err := s.workCoverOf(r.Context(), ref)
	if err != nil {
		obs.ServerError(w, "load the work's cover failed", err)
		httpx.Error(w, "load the work's cover failed", http.StatusBadGateway)
		return
	}
	if !found {
		httpx.Error(w, "this work has no cover", http.StatusNotFound)
		return
	}
	writeCover(w, r, cover)
}

// handleWorkFile serves a work version's file to a caller who may read
// the component: its one file (a work that is not a site; the detail's
// viewers render it), or with ?path= one of its recipe's reference files.
// The bytes are typed by their name (store.ArtifactMIME) and served as an
// artifact's are: nosniff, a sandbox CSP, inline only for a raster image.
func (s *Service) handleWorkFile(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	work, ok := s.workVersion(w, r, caller, false)
	if !ok {
		return
	}
	name, data, ok := contentpkg.WorkFile(work.pkg)
	if p := r.URL.Query().Get("path"); p != "" {
		ok = false
		for _, ref := range contentpkg.WorkReferences(work.pkg) {
			if ref.Ref.File == p {
				name, data, ok = ref.Ref.Name, ref.Data, true
			}
		}
	}
	if !ok {
		httpx.Error(w, "no such file in this work", http.StatusNotFound)
		return
	}
	mt := store.ArtifactMIME(name, data[:min(len(data), 512)])
	h := w.Header()
	h.Set("Content-Type", mt)
	disposition := "attachment"
	if store.IsInlineSafeImage(mt) {
		disposition = "inline"
	}
	h.Set("Content-Disposition", httpx.ContentDisposition(disposition, name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// A version never changes: its files are cached by the member's browser.
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// handleFavorite stars (PUT) a component visible to the caller, or takes
// the star back (DELETE, whatever the component's visibility now), in the
// organization they act in.
func (s *Service) handleFavorite(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims, ctx, id := actor.Claims, r.Context(), r.PathValue("id")
	if r.Method == http.MethodDelete {
		if err := s.ContentComponents.DeleteContentFavorite(ctx, claims.Tenant, claims.Subject, id); err != nil {
			obs.ServerError(w, "remove favorite failed", err)
			httpx.Error(w, "remove favorite failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	c, err := s.ContentComponents.GetContentComponent(ctx, id)
	if err == nil && !extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Visible {
		err = store.ErrContentNotFound
	}
	if err == nil {
		err = s.ContentComponents.PutContentFavorite(ctx, store.ContentFavorite{TenantID: claims.Tenant, Member: claims.Subject, ComponentID: c.ID, CreatedAtMs: s.nowTime().UnixMilli()})
	}
	if err != nil {
		contentStoreErr(w, "save favorite failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUseApp copies a version of an app work visible to the caller into
// a room they own (roomId: a member who may chat in it) or into a new room
// they create (newRoom): a new site artifact whose first version is the
// app's site, attributed "from <name>@<version>". Nothing of the
// original's data comes along: the copy's data space is its own new
// artifact id's, empty. The copy counts as a use of the work in the room's
// organization (content_component_uses, scope app-copy).
func (s *Service) handleUseApp(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims, ctx := actor.Claims, r.Context()
	var in wire.AppUseRequest
	c, v, pkg, ok := s.galleryWork(w, r, claims, &in.Version, &in)
	if !ok {
		return
	}
	if pkg.Manifest.Type != wire.WorkTypeApp {
		httpx.Error(w, fmt.Sprintf("%s is a %s: only an app is used as a copy", c.Name, pkg.Manifest.Type), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, appCopyTimeout)
	defer cancel()
	reply := wire.AppUseReply{From: c.Name + "@" + v.Version}
	owner, roomID, created, ok := s.galleryRoom(ctx, w, claims, in.RoomID, in.NewRoom, firstNonEmpty(c.DisplayName, c.Name))
	if !ok {
		return
	}
	reply.RoomID, reply.CreatedRoom = roomID, created
	var err error
	if reply.ArtifactID, reply.Version, err = s.copyApp(ctx, claims.Subject, owner, roomID, c, v, pkg); err != nil {
		s.writeRoomBackendErr(w, "copy the app into the room failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, reply)
}

// copyApp copies an app work's version (pkg, an app) into roomID as a new
// site of the member in owner (copySite: a fresh, empty data space) and
// counts the copy as a use of the work (scope app-copy) by actor: the one
// copy of an app, for "Use this app" and a room package's install alike.
// It answers the new site's artifact id and version.
func (s *Service) copyApp(ctx context.Context, actor string, owner auth.Claims, roomID string, c store.ContentComponent, v store.ContentComponentVersion, pkg *contentpkg.Package) (string, int, error) {
	snapshot, err := tarbundle.PackFiles(contentpkg.WorkSite(pkg), tarbundle.SiteLimits)
	if err != nil {
		return "", 0, fmt.Errorf("pack the app's site: %w", err)
	}
	from := c.Name + "@" + v.Version
	artifactID, version, err := s.copySite(ctx, owner, roomID, firstNonEmpty(c.DisplayName, c.Name), c.Name, from, snapshot)
	if err != nil {
		return "", 0, err
	}
	if err := s.ContentComponents.PutContentUse(ctx, store.ContentUse{TenantID: owner.Tenant, ScopeKind: store.ContentUseAppCopy, ScopeID: artifactID,
		ComponentID: c.ID, Kind: c.Kind, PinnedVersion: v.Version, CreatedBy: actor, CreatedAtMs: s.nowTime().UnixMilli()}); err != nil {
		return "", 0, fmt.Errorf("count the app's use: %w", err)
	}
	s.auditContent(ctx, "use", actor, owner.Tenant, c, v.Version,
		contentshare.Label(c)+" v"+v.Version+" copied by "+actor+" into room "+roomID+" as site "+artifactID)
	return artifactID, version, nil
}

// galleryWork reads a gallery request on a work: the work visible to the
// caller (extpipe.MemberVisibility; anything else a 404), the request body
// into in, and the version *version names (default the latest), loaded and
// validated. It answers the refusal.
func (s *Service) galleryWork(w http.ResponseWriter, r *http.Request, claims auth.Claims, version *string, in any) (store.ContentComponent, store.ContentComponentVersion, *contentpkg.Package, bool) {
	ctx := r.Context()
	c, err := s.ContentComponents.GetContentComponent(ctx, r.PathValue("id"))
	if err == nil && !extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Visible {
		err = store.ErrContentNotFound
	}
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return store.ContentComponent{}, store.ContentComponentVersion{}, nil, false
	}
	if c.Kind != contentpkg.KindWork {
		httpx.Error(w, fmt.Sprintf("%s is a %s, not a work", c.Name, shortKind(c.Kind)), http.StatusBadRequest)
		return store.ContentComponent{}, store.ContentComponentVersion{}, nil, false
	}
	raw, ok := readBody(w, r, maxWorkRequestBytes)
	if !ok {
		return store.ContentComponent{}, store.ContentComponentVersion{}, nil, false
	}
	if err := json.Unmarshal(raw, in); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return store.ContentComponent{}, store.ContentComponentVersion{}, nil, false
	}
	*version = firstNonEmpty(*version, c.LatestVersion)
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, *version)
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return store.ContentComponent{}, store.ContentComponentVersion{}, nil, false
	}
	pkg, err := s.contentPackage(ctx, v)
	if err != nil {
		obs.ServerError(w, "load content bundle failed", err)
		httpx.Error(w, "load content bundle failed", http.StatusBadGateway)
		return store.ContentComponent{}, store.ContentComponentVersion{}, nil, false
	}
	return c, v, pkg, true
}

// galleryRoom is the room a gallery action lands in: roomID, a room the
// caller owns (one they may post in), or a new room of theirs (newRoom,
// named name unless it names one); exactly one of the two. owner is the
// caller acting in the room's organization. It answers the refusal.
func (s *Service) galleryRoom(ctx context.Context, w http.ResponseWriter, claims auth.Claims, roomID string, newRoom *wire.WorkNewRoom, name string) (owner auth.Claims, room string, created bool, ok bool) {
	roomID = strings.TrimSpace(roomID)
	if (roomID == "") == (newRoom == nil) {
		httpx.Error(w, "name one room: roomId (a room you own) or newRoom", http.StatusBadRequest)
		return auth.Claims{}, "", false, false
	}
	if roomID != "" {
		rc, reach := s.roomClaims(claims, roomID)
		if m, member := s.roomMember(claims.Subject, roomID); !reach || !member || !m.MayChat() {
			httpx.Error(w, "pick a room you own (one you may post in), or a new room", http.StatusForbidden)
			return auth.Claims{}, "", false, false
		}
		return rc, roomID, false, true
	}
	roomID = ids.New("room-")
	create, _ := json.Marshal(map[string]any{"roomId": roomID, "name": firstNonEmpty(newRoom.Name, name)})
	if err := s.roomBackendCall(ctx, http.MethodPost, "/v1/rooms", claims.Subject, claims.Tenant, create); err != nil {
		s.writeRoomBackendErr(w, "create the room failed", err)
		return auth.Claims{}, "", false, false
	}
	return claims, roomID, true, true
}

// copySite creates a site artifact in roomID as the member in claims and
// publishes snapshot as its first version ("from <from>"), through the
// room service's own artifact routes and checks. It answers the artifact
// id and version.
func (s *Service) copySite(ctx context.Context, claims auth.Claims, roomID, title, fileName, from string, snapshot []byte) (string, int, error) {
	return s.createRoomArtifact(ctx, claims, roomID, store.ArtifactKindSite, title, fileName, tarbundle.SiteMIME, from, snapshot)
}

// createRoomArtifact is the gateway's one creation of a room deliverable
// from bytes: an artifact of kind in roomID as the member in claims, its
// first version data of contentType ("from <from>"), through the room
// service's own artifact routes and checks (a site's copy, a room
// package's file). It answers the artifact id and version.
func (s *Service) createRoomArtifact(ctx context.Context, claims auth.Claims, roomID, kind, title, fileName, contentType, from string, data []byte) (string, int, error) {
	base := "/v1/rooms/" + url.PathEscape(roomID) + "/artifacts"
	create, _ := json.Marshal(map[string]any{"kind": kind, "title": title, "fileName": fileName})
	var created wire.ArtifactReply
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodPost, base, "application/json", create, claims.Subject, claims.Tenant, &created); err != nil {
		return "", 0, err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, f := range [][2]string{{"baseVersion", "0"}, {"summary", "from " + from}} {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return "", 0, err
		}
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": fileName}))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", 0, err
	}
	if _, err := part.Write(data); err != nil {
		return "", 0, err
	}
	if err := mw.Close(); err != nil {
		return "", 0, err
	}
	var published wire.ArtifactReply
	if err := s.roomBackendDo(ctx, backendStreamHTTP, http.MethodPost, base+"/"+url.PathEscape(created.Artifact.ArtifactID)+"/versions",
		mw.FormDataContentType(), body.Bytes(), claims.Subject, claims.Tenant, &published); err != nil {
		return "", 0, err
	}
	return created.Artifact.ArtifactID, published.Artifact.CurrentVersion, nil
}

// writeRoomBackendErr answers a failed room service call: a refusal the
// member can act on keeps its status and reason (the room is not theirs to
// write, gone, or the copy is over the room's cap), anything else is a
// logged 502.
func (s *Service) writeRoomBackendErr(w http.ResponseWriter, what string, err error) {
	var rbe *roomBackendError
	if errors.As(err, &rbe) {
		switch rbe.Status {
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge:
			httpx.WriteStatus(w, rbe.Status, rbe.Body, nil)
			return
		}
	}
	obs.ServerError(w, what, err)
	httpx.Error(w, what, http.StatusBadGateway)
}

// workTitle is a work's default title: the deliverable's own title, unless
// that is a workspace path or the file name (an agent's file is titled by
// its path): then the file's name without its extension, words apart
// ("work/out/Q3_sales-report.pptx" → "Q3 sales report").
func workTitle(art wire.Artifact) string {
	title := strings.TrimSpace(art.Title)
	if title != "" && !strings.Contains(title, "/") && title != art.FileName {
		return title
	}
	name := path.Base(firstNonEmpty(art.FileName, title))
	if ext := path.Ext(name); ext != "" && len(ext) < len(name) {
		name = strings.TrimSuffix(name, ext)
	}
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' }), " ")
	return firstNonEmpty(name, title)
}
