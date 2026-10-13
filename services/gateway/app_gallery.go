package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
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

// The app gallery's server side (docs/prd/2026-10-13-app-gallery.md §3,
// §6, §7, §8): an interactive app is a content component of kind "app"
// (contentpkg), so scopes, org approval, platform signing, visibility
// (contentuse.Visible) and the Library apply to it unchanged. These routes
// add what only an app has:
//
//	POST /v1/rooms/{roomId}/artifacts/{artifactId}/publish-app   a site's current version as an app of the caller's
//	GET  /v1/content-components/{id}/versions/{version}/cover    an app's cover image
//	PUT|DELETE /v1/content-components/{id}/favorite              the caller's star
//	POST /v1/content-components/{id}/use-app                     a copy as a new site in a room the caller owns
//
// A template never holds data: publishing snapshots the site's files (the
// room service's site version, read as the member), and a copy is a new
// site artifact whose data space, keyed by its new id, starts empty.

// Bounds of the gallery's requests.
const (
	maxAppRequestBytes = 16 << 10
	// appCopyTimeout bounds a copy's room calls (a site snapshot upload of
	// up to tarbundle.SiteLimits).
	appCopyTimeout = 2 * time.Minute
)

// handlePublishApp snapshots a site artifact's current version (its files,
// cover and README, never its data) as an app component of the caller's: a
// new one, or the next version of one of theirs (componentId). Only an
// owner of the room's apps publishes one: a member who may chat in it
// (membership.Member.MayChat, the app data owner rule). With ?job= the
// bridge running the job publishes for its requester (jobGrant, the
// content routes' admission; save_component with artifactId).
func (s *Service) handlePublishApp(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	ctx := r.Context()
	roomID, artifactID := r.PathValue("roomId"), r.PathValue("artifactId")
	claims, ok := s.roomClaims(caller, roomID)
	if !ok {
		httpx.Error(w, "room scope not authorized", http.StatusForbidden)
		return
	}
	actor := contentActor{Claims: claims, Room: roomID}
	if job := strings.TrimSpace(r.URL.Query().Get("job")); job != "" {
		var err error
		if actor, err = s.jobGrant(ctx, caller, claims, roomID, job); err != nil {
			writeGrantErr(w, err)
			return
		}
	}
	if m, ok := s.roomMember(actor.Subject, roomID); !ok || !m.MayChat() {
		httpx.Error(w, "only the room's members who may post in it (its apps' owners) publish its apps", http.StatusForbidden)
		return
	}
	raw, ok := readBody(w, r, maxAppRequestBytes)
	if !ok {
		return
	}
	var in wire.AppPublishRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	art, site, err := s.currentSite(ctx, actor.Claims, roomID, artifactID)
	switch {
	case errors.Is(err, errSiteNotFound):
		httpx.Error(w, "no published site with that id in this room", http.StatusNotFound)
		return
	case err != nil:
		obs.ServerError(w, "load site failed", err)
		httpx.Error(w, "load site failed", http.StatusBadGateway)
		return
	}
	m := contentpkg.Manifest{
		Name: strings.TrimSpace(in.Name), Version: "1.0.0",
		DisplayName: firstNonEmpty(in.DisplayName, art.Title), Description: strings.TrimSpace(in.Description),
		Category: strings.TrimSpace(in.Category), Tags: in.Tags,
	}
	var existing *store.ContentComponent
	if id := strings.TrimSpace(in.ComponentID); id != "" {
		c, err := s.ownContent(ctx, actor.Claims, id)
		if err != nil {
			contentStoreErr(w, "load content component failed", err)
			return
		}
		if c.Kind != contentpkg.KindApp {
			httpx.Error(w, fmt.Sprintf("%s is a %s, not an app", c.Name, shortKind(c.Kind)), http.StatusBadRequest)
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
		m.DisplayName = firstNonEmpty(in.DisplayName, prev.DisplayName, art.Title)
		m.Description = firstNonEmpty(in.Description, prev.Description)
		m.Category = firstNonEmpty(in.Category, prev.Category)
		if len(m.Tags) == 0 {
			m.Tags = prev.Tags
		}
		m.Name, existing = c.Name, &c
	}
	if m.Name == "" {
		m.Name = firstNonEmpty(contentpkg.NameFrom(m.DisplayName), contentpkg.NameFrom(art.FileName), "app")
	}
	pkg, bundle, err := contentpkg.AppFromSite(site, m)
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

// currentSite reads a site artifact's current version as the member in
// claims (the room service's own membership and artifact checks decide):
// the artifact and its snapshot. errSiteNotFound when the artifact is not
// a published site of the room the member reads.
func (s *Service) currentSite(ctx context.Context, claims auth.Claims, roomID, artifactID string) (wire.Artifact, *tarbundle.Bundle, error) {
	var detail wire.ArtifactDetail
	err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/artifacts/"+url.PathEscape(artifactID),
		"", nil, claims.Subject, claims.Tenant, &detail)
	var rbe *roomBackendError
	switch {
	case errors.As(err, &rbe) && rbe.Status < http.StatusInternalServerError:
		return wire.Artifact{}, nil, errSiteNotFound
	case err != nil:
		return wire.Artifact{}, nil, err
	}
	art := detail.Artifact
	if art.Kind != store.ArtifactKindSite || art.CurrentVersion < 1 {
		return wire.Artifact{}, nil, errSiteNotFound
	}
	sv, err := s.memberSiteVersion(ctx, claims.Subject, claims.Tenant, siteTicketScope(roomID, art.ArtifactID, art.CurrentVersion))
	if err != nil {
		return wire.Artifact{}, nil, err
	}
	b, err := s.siteBundle(ctx, sv)
	if err != nil {
		return wire.Artifact{}, nil, err
	}
	return art, b, nil
}

// handleAppCover serves an app version's cover image to a caller who may
// read the component (readableContent), as the site cover route does
// (tarbundle.SiteCover, writeCover). 404 when the app ships none, so a card
// falls back to its generated cover.
func (s *Service) handleAppCover(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	ctx := r.Context()
	c, err := s.readableContent(ctx, actor.Claims, r.PathValue("id"), actor.Room)
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	if c.Kind != contentpkg.KindApp {
		httpx.Error(w, "only an app has a cover", http.StatusNotFound)
		return
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, r.PathValue("version"))
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return
	}
	pkg, err := s.contentPackage(ctx, v)
	if err != nil {
		obs.ServerError(w, "load content bundle failed", err)
		httpx.Error(w, "load content bundle failed", http.StatusBadGateway)
		return
	}
	// The validator accepted the version's cover (tarbundle.SiteCover).
	cover, found, err := tarbundle.SiteCover(pkg.Bundle)
	if !found || err != nil {
		httpx.Error(w, "this app has no cover", http.StatusNotFound)
		return
	}
	writeCover(w, r, cover)
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

// handleUseApp copies a version of an app visible to the caller into a
// room they own (roomId: a member who may chat in it) or into a new room
// they create (newRoom): a new site artifact whose first version is the
// app's site, attributed "from <name>@<version>". Nothing of the
// original's data comes along: the copy's data space is its own new
// artifact id's, empty. The copy counts as a use of the app in the room's
// organization (content_component_uses, scope app-copy).
func (s *Service) handleUseApp(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims, ctx := actor.Claims, r.Context()
	c, err := s.ContentComponents.GetContentComponent(ctx, r.PathValue("id"))
	if err == nil && !extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Visible {
		err = store.ErrContentNotFound
	}
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	if c.Kind != contentpkg.KindApp {
		httpx.Error(w, fmt.Sprintf("%s is a %s: only an app is used as a copy", c.Name, shortKind(c.Kind)), http.StatusBadRequest)
		return
	}
	raw, ok := readBody(w, r, maxAppRequestBytes)
	if !ok {
		return
	}
	var in wire.AppUseRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	roomID := strings.TrimSpace(in.RoomID)
	if (roomID == "") == (in.NewRoom == nil) {
		httpx.Error(w, "name one room: roomId (a room you own) or newRoom", http.StatusBadRequest)
		return
	}
	in.Version = firstNonEmpty(in.Version, c.LatestVersion)
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, in.Version)
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return
	}
	pkg, err := s.contentPackage(ctx, v)
	if err != nil {
		obs.ServerError(w, "load content bundle failed", err)
		httpx.Error(w, "load content bundle failed", http.StatusBadGateway)
		return
	}
	snapshot, err := tarbundle.PackFiles(contentpkg.AppSite(pkg), tarbundle.SiteLimits)
	if err != nil {
		obs.ServerError(w, "pack the app's site failed", err)
		httpx.Error(w, "pack the app's site failed", http.StatusInternalServerError)
		return
	}
	title := firstNonEmpty(c.DisplayName, c.Name)
	ctx, cancel := context.WithTimeout(ctx, appCopyTimeout)
	defer cancel()
	reply := wire.AppUseReply{From: c.Name + "@" + v.Version}
	// owner is the caller acting in the room's organization.
	var owner auth.Claims
	if roomID != "" {
		rc, reach := s.roomClaims(claims, roomID)
		if m, member := s.roomMember(claims.Subject, roomID); !reach || !member || !m.MayChat() {
			httpx.Error(w, "use an app in a room you own (one you may post in), or in a new room", http.StatusForbidden)
			return
		}
		owner = rc
	} else {
		roomID = ids.New("room-")
		name := firstNonEmpty(in.NewRoom.Name, title)
		create, _ := json.Marshal(map[string]any{"roomId": roomID, "name": name})
		if err := s.roomBackendCall(ctx, http.MethodPost, "/v1/rooms", claims.Subject, claims.Tenant, create); err != nil {
			s.writeRoomBackendErr(w, "create the room failed", err)
			return
		}
		owner, reply.CreatedRoom = claims, true
	}
	reply.RoomID = roomID
	if reply.ArtifactID, reply.Version, err = s.copySite(ctx, owner, roomID, title, c.Name, reply.From, snapshot); err != nil {
		s.writeRoomBackendErr(w, "copy the app into the room failed", err)
		return
	}
	now := s.nowTime().UnixMilli()
	if err := s.ContentComponents.PutContentUse(ctx, store.ContentUse{TenantID: owner.Tenant, ScopeKind: store.ContentUseAppCopy, ScopeID: reply.ArtifactID,
		ComponentID: c.ID, Kind: c.Kind, PinnedVersion: v.Version, CreatedBy: claims.Subject, CreatedAtMs: now}); err != nil {
		obs.ServerError(w, "count the app's use failed", err)
		httpx.Error(w, "count the app's use failed", http.StatusInternalServerError)
		return
	}
	s.auditContent(ctx, "use", claims.Subject, owner.Tenant, c, v.Version,
		contentshare.Label(c)+" v"+v.Version+" copied by "+claims.Subject+" into room "+roomID+" as site "+reply.ArtifactID)
	httpx.WriteJSON(w, http.StatusCreated, reply)
}

// copySite creates a site artifact in roomID as the member in claims and
// publishes snapshot as its first version ("from <from>"), through the
// room service's own artifact routes and checks. It answers the artifact
// id and version.
func (s *Service) copySite(ctx context.Context, claims auth.Claims, roomID, title, fileName, from string, snapshot []byte) (string, int, error) {
	base := "/v1/rooms/" + url.PathEscape(roomID) + "/artifacts"
	create, _ := json.Marshal(map[string]any{"kind": store.ArtifactKindSite, "title": title, "fileName": fileName})
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
	h.Set("Content-Type", tarbundle.SiteMIME)
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", 0, err
	}
	if _, err := part.Write(snapshot); err != nil {
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
