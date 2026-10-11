package room

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Collaboration deliverables (specs, reports, code) as first-class room objects.
// Humans publish through the room UI and agents through the same API under the
// owner's token, so one surface serves both. Authorization is room membership:
// an artifact is visible to exactly the people already in the room.

// maxArtifactContentBytes bounds one inline (text) revision; larger and
// binary files publish as blob versions (artifact_files.go) under the
// deployment's ArtifactMaxBytes.
const maxArtifactContentBytes = store.ArtifactInlineMaxBytes

// maxArtifactTitleRunes keeps the panel readable and the index cheap.
const maxArtifactTitleRunes = 200

type createArtifactBody struct {
	// ArtifactId optionally carries a caller-chosen stable id (bridge-derived).
	ArtifactId string `json:"artifactId"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	ChannelId  string `json:"channelId"`
	Language   string `json:"language"`
	// FileName is the deliverable's file name (display + extension source).
	FileName string `json:"fileName"`
	Content  string `json:"content"`
	Summary  string `json:"summary"`
}

type publishVersionBody struct {
	BaseVersion int    `json:"baseVersion"`
	Content     string `json:"content"`
	Summary     string `json:"summary"`
	SourceJobID string `json:"sourceJobId"`
	SourceSeq   int64  `json:"sourceSeq"`
}

// Bounds of the version provenance and file-name fields.
const (
	maxArtifactFileNameRunes = 256
	maxArtifactSummaryRunes  = 512
	maxArtifactSourceJobLen  = 128
)

// artifactIDShape accepts server-minted and bridge-derived ids: lowercase
// letters, digits, and underscores.
var artifactIDShape = regexp.MustCompile(`^[a-z0-9_]+$`)

// errSiteNeedsSnapshot refuses inline content for a site: its versions are
// tar snapshots published as a multipart file part.
const errSiteNeedsSnapshot = "a site version is a " + tarbundle.SiteMIME + " snapshot published as a multipart file part"

// siteTypeMismatch reports a version type that breaks the site pairing: a
// site's versions are tar snapshots (tarbundle.SiteMIME) and only a site
// carries that type — the site routes serve its files one by one under a
// sandboxed origin, while the content route serves the tar itself as an
// attachment, never as a document.
func siteTypeMismatch(kind, mimeType string) bool {
	return (kind == store.ArtifactKindSite) != (store.MediaType(mimeType) == tarbundle.SiteMIME)
}

func validArtifactKind(k string) bool {
	switch k {
	case store.ArtifactKindMarkdown, store.ArtifactKindCode, store.ArtifactKindImage, store.ArtifactKindHTML, store.ArtifactKindApp, store.ArtifactKindFile, store.ArtifactKindSite:
		return true
	}
	return false
}

func (s *Service) handleCreateArtifact(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	userID := caller(r)
	if !s.Store.IsMember(r.Context(), roomID, userID) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	var body createArtifactBody
	if !decodeArtifactBody(w, r, &body) {
		return
	}
	body.Kind = strings.TrimSpace(body.Kind)
	body.Title = strings.TrimSpace(body.Title)
	if !validArtifactKind(body.Kind) {
		writeErr(w, http.StatusBadRequest, "kind must be one of markdown, code, image, html, app, file, site")
		return
	}
	if body.Kind == store.ArtifactKindSite && body.Content != "" {
		writeErr(w, http.StatusBadRequest, errSiteNeedsSnapshot)
		return
	}
	if body.Title == "" {
		writeErr(w, http.StatusBadRequest, "title is required")
		return
	}
	if len([]rune(body.Title)) > maxArtifactTitleRunes {
		writeErr(w, http.StatusBadRequest, "title is too long")
		return
	}
	if len(body.Content) > maxArtifactContentBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "content exceeds the 2 MiB limit")
		return
	}
	body.FileName = sanitizeArtifactFileName(body.FileName)
	if len([]rune(body.FileName)) > maxArtifactFileNameRunes {
		writeErr(w, http.StatusBadRequest, "fileName is too long")
		return
	}
	now := s.Now()
	id := newArtifactID()
	// A caller may carry its own stable id (the bridge derives one per session
	// file, so an agent's turns publish versions of ONE artifact instead of a
	// duplicate per turn). Server-minted when absent; shape-validated when
	// present, and the store's idempotent put keeps any existing history.
	if callerID := strings.TrimSpace(body.ArtifactId); callerID != "" {
		if len(callerID) < 8 || len(callerID) > 64 || !artifactIDShape.MatchString(callerID) {
			writeErr(w, http.StatusBadRequest, "invalid artifactId")
			return
		}
		id = callerID
	}
	art, err := s.Artifacts.PutArtifact(r.Context(), store.Artifact{
		ArtifactID:  id,
		RoomID:      roomID,
		ChannelID:   strings.TrimSpace(body.ChannelId),
		Kind:        body.Kind,
		Title:       body.Title,
		Language:    strings.TrimSpace(body.Language),
		FileName:    body.FileName,
		CreatedBy:   userID,
		CreatedAtMs: now,
		UpdatedAtMs: now,
	})
	if err != nil {
		obs.ServerError(w, "create artifact failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// An artifact with content publishes its first version immediately, so the
	// create flow is one step for a member pasting a document.
	if body.Content != "" {
		_, v, err := s.Artifacts.PublishArtifactVersion(r.Context(), art.ArtifactID, store.ArtifactVersion{
			Content: body.Content, MimeType: store.ArtifactInlineMIME(art),
			Summary: strings.TrimSpace(body.Summary), AuthorUserID: userID, CreatedAtMs: s.Now(),
		}, 0)
		if err != nil {
			obs.ServerError(w, "create artifact failed", err)
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		art.CurrentVersion = v.Version
		art.UpdatedAtMs = v.CreatedAtMs
		art.MimeType = v.MimeType
	}
	s.publishArtifactEvent(r.Context(), art, userID)
	writeJSON(w, http.StatusOK, map[string]any{"artifact": art})
}

// handleListArtifacts lists the room's artifacts, newest-updated first, with
// filters (channelId, kind, mime — exact or "type/*" —, author, sourceSeq, q)
// and keyset paging (limit ≤ 200, default 100; nextCursor → cursor). maxBytes
// is the blob version cap, so publishers skip files the room would refuse.
func (s *Service) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.Store.IsMember(r.Context(), roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	q, ok := parseArtifactQuery(w, r, roomID)
	if !ok {
		return
	}
	arts, err := s.Artifacts.ListArtifacts(r.Context(), q)
	if err != nil {
		obs.ServerError(w, "list artifacts failed", err)
		writeErr(w, http.StatusInternalServerError, "list artifacts failed")
		return
	}
	resp := map[string]any{"artifacts": arts, "maxBytes": s.artifactMaxBytes()}
	if len(arts) == q.Limit {
		last := arts[len(arts)-1]
		resp["nextCursor"] = strconv.FormatInt(last.UpdatedAtMs, 10) + ":" + last.ArtifactID
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleArtifactAnchors answers which artifacts were produced by the room
// messages with seq in [from, to]: GET .../artifact-anchors?from=&to= →
// {anchors: [{seq, artifactId}]}. The feed anchors its deliverable cards
// under exactly the messages it has loaded, however old.
func (s *Service) handleArtifactAnchors(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.Store.IsMember(r.Context(), roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	from, errFrom := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, errTo := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if errFrom != nil || errTo != nil || from < 1 || to < from {
		writeErr(w, http.StatusBadRequest, "from and to must be sequences with 1 <= from <= to")
		return
	}
	anchors, err := s.Artifacts.ArtifactAnchors(r.Context(), roomID, from, to)
	if err != nil {
		obs.ServerError(w, "list artifact anchors failed", err)
		writeErr(w, http.StatusInternalServerError, "list artifact anchors failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"anchors": anchors})
}

// parseArtifactQuery reads the list filters and paging cursor.
func parseArtifactQuery(w http.ResponseWriter, r *http.Request, roomID string) (store.ArtifactQuery, bool) {
	v := r.URL.Query()
	q := store.ArtifactQuery{
		RoomID:    roomID,
		ChannelID: strings.TrimSpace(v.Get("channelId")),
		Kind:      strings.TrimSpace(v.Get("kind")),
		MimeType:  store.MediaType(v.Get("mime")),
		Author:    strings.TrimSpace(v.Get("author")),
		Text:      strings.TrimSpace(v.Get("q")),
		Limit:     100,
	}
	if raw := strings.TrimSpace(v.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 200 {
			writeErr(w, http.StatusBadRequest, "limit must be 1..200")
			return q, false
		}
		q.Limit = n
	}
	if raw := strings.TrimSpace(v.Get("sourceSeq")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid sourceSeq")
			return q, false
		}
		q.SourceSeq = n
	}
	if raw := strings.TrimSpace(v.Get("cursor")); raw != "" {
		ms, id, ok := strings.Cut(raw, ":")
		n, err := strconv.ParseInt(ms, 10, 64)
		if !ok || err != nil || n <= 0 || id == "" {
			writeErr(w, http.StatusBadRequest, "invalid cursor")
			return q, false
		}
		q.BeforeUpdatedMs, q.BeforeID = n, id
	}
	return q, true
}

func (s *Service) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.authorizeArtifactRead(w, r, roomID) {
		return
	}
	art, err := s.Artifacts.GetArtifact(r.Context(), r.PathValue("artifactId"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "artifact not found")
		return
	}
	versions := s.Artifacts.ListArtifactVersions(r.Context(), art.ArtifactID, 50)
	writeJSON(w, http.StatusOK, map[string]any{"artifact": art, "versions": versions})
}

func (s *Service) handleGetArtifactVersion(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.authorizeArtifactRead(w, r, roomID) {
		return
	}
	art, err := s.Artifacts.GetArtifact(r.Context(), r.PathValue("artifactId"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "artifact not found")
		return
	}
	version, perr := strconv.Atoi(strings.TrimSpace(r.PathValue("version")))
	if perr != nil {
		writeErr(w, http.StatusBadRequest, "invalid version")
		return
	}
	v, err := s.Artifacts.GetArtifactVersion(r.Context(), art.ArtifactID, version)
	if err != nil {
		writeErr(w, http.StatusNotFound, "version not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v})
}

func (s *Service) handlePublishVersion(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	userID := caller(r)
	if !s.Store.IsMember(r.Context(), roomID, userID) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	art, err := s.Artifacts.GetArtifact(r.Context(), r.PathValue("artifactId"))
	if err != nil || art.RoomID != roomID {
		writeErr(w, http.StatusNotFound, "artifact not found")
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		s.publishBlobVersion(w, r, art, userID)
		return
	}
	if art.Kind == store.ArtifactKindSite {
		writeErr(w, http.StatusBadRequest, errSiteNeedsSnapshot)
		return
	}
	var body publishVersionBody
	if !decodeArtifactBody(w, r, &body) {
		return
	}
	if !validVersionProvenance(w, body.Summary, body.SourceJobID, body.SourceSeq) {
		return
	}
	if len(body.Content) > maxArtifactContentBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "content exceeds the 2 MiB limit")
		return
	}
	if body.BaseVersion < 0 {
		writeErr(w, http.StatusBadRequest, "baseVersion must be >= 0")
		return
	}
	s.commitVersion(w, r, art, userID, store.ArtifactVersion{
		Content: body.Content, MimeType: store.ArtifactInlineMIME(art),
		SourceJobID: strings.TrimSpace(body.SourceJobID), SourceSeq: body.SourceSeq,
		Summary: strings.TrimSpace(body.Summary), AuthorUserID: userID, CreatedAtMs: s.Now(),
	}, body.BaseVersion)
}

// validVersionProvenance bounds a version's summary and source link.
func validVersionProvenance(w http.ResponseWriter, summary, sourceJobID string, sourceSeq int64) bool {
	if len([]rune(strings.TrimSpace(summary))) > maxArtifactSummaryRunes {
		writeErr(w, http.StatusBadRequest, "summary is too long")
		return false
	}
	if len(strings.TrimSpace(sourceJobID)) > maxArtifactSourceJobLen || sourceSeq < 0 {
		writeErr(w, http.StatusBadRequest, "invalid sourceJobId or sourceSeq")
		return false
	}
	return true
}

// commitVersion publishes v on baseVersion (CAS) and announces it.
func (s *Service) commitVersion(w http.ResponseWriter, r *http.Request, art store.Artifact, userID string, v store.ArtifactVersion, baseVersion int) {
	art, v, err := s.Artifacts.PublishArtifactVersion(r.Context(), art.ArtifactID, v, baseVersion)
	if err != nil {
		if errors.Is(err, store.ErrArtifactVersionConflict) {
			// 409 carries the current version so the client can re-read and
			// rebase instead of guessing: concurrent editors see the race.
			writeErr(w, http.StatusConflict, "artifact was updated concurrently")
			return
		}
		obs.ServerError(w, "publish version failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publishArtifactEvent(r.Context(), art, userID)
	writeJSON(w, http.StatusOK, map[string]any{"artifact": art, "version": v})
}

// authorizeArtifactRead checks room membership and that the artifact belongs to
// the room in the path, so an id leaked across rooms cannot be probed.
func (s *Service) authorizeArtifactRead(w http.ResponseWriter, r *http.Request, roomID string) bool {
	if !s.Store.IsMember(r.Context(), roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return false
	}
	art, err := s.Artifacts.GetArtifact(r.Context(), r.PathValue("artifactId"))
	if err != nil || art.RoomID != roomID {
		writeErr(w, http.StatusNotFound, "artifact not found")
		return false
	}
	return true
}

// publishArtifactEvent tells the room's subscribers (web panels, bridges) that
// an artifact changed, so previews refresh live. Metadata only: the SSE stream
// never carries content.
func (s *Service) publishArtifactEvent(ctx context.Context, art store.Artifact, actor string) {
	s.publishArtifactEventReason(ctx, art, actor, "")
}

// publishArtifactEventReason is publishArtifactEvent with a reason for a
// change that is not a new version ("thumbnail": a version thumbnail was
// stored, so file cards refetch it).
func (s *Service) publishArtifactEventReason(ctx context.Context, art store.Artifact, actor, reason string) {
	if s.Publisher == nil {
		return
	}
	payload := map[string]any{
		"artifactId":     art.ArtifactID,
		"kind":           art.Kind,
		"title":          art.Title,
		"mimeType":       art.MimeType,
		"fileName":       art.FileName,
		"currentVersion": art.CurrentVersion,
		"updatedBy":      actor,
		"updatedAtMs":    art.UpdatedAtMs,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	// Artifact events deliver room-wide (empty ChannelID): the Docs panel is a
	// room-scoped surface, so a viewer sitting in another channel must still
	// see version bumps the moment they land — channel-filtering them makes a
	// watched artifact look frozen until the 30s list poll happens to fire.
	s.Publisher.Publish(ctx, wire.Event{
		RoomID:   art.RoomID,
		Kind:     "artifact_updated",
		Payload:  payload,
		AtUnixMs: art.UpdatedAtMs,
	})
}

func decodeArtifactBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxArtifactContentBytes+(64<<10))).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

// newArtifactID mints a server-side id (same shape as the room service's other
// opaque ids).
func newArtifactID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "art-fallback"
	}
	return "art_" + hex.EncodeToString(b[:])
}

// maxArtifactStateBytes bounds one collaboration-state payload. The state is
// transient (never persisted) but fans out to every room subscriber on each
// change, so it must stay gesture-sized, not document-sized.
const maxArtifactStateBytes = 16 * 1024

type artifactStateBody struct {
	ArtifactID string          `json:"artifactId"`
	State      json.RawMessage `json:"state"`
}

// handleArtifactState relays a live artifact's collaboration state to the
// room: what a member's running preview (or an agent's simulation, or
// anything hosting the artifact) just did — a diagram annotation, a game
// move, a dashboard filter. It is deliberately NOT durable: state is the
// artifact's live session, not a document revision; persistence is the
// version API. Authorization mirrors artifact reads (room membership plus
// the artifact actually living in that room), and the payload is bounded so
// a runaway guest cannot bloat every subscriber's stream.
func (s *Service) handleArtifactState(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	userID := caller(r)
	if !s.Store.IsMember(r.Context(), roomID, userID) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	var body artifactStateBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxArtifactStateBytes+(4<<10))).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	// The path carries the artifact id (the client already knows it there); a
	// body artifactId is accepted for symmetry but the path wins.
	if pathID := strings.TrimSpace(r.PathValue("artifactId")); pathID != "" {
		body.ArtifactID = pathID
	} else {
		body.ArtifactID = strings.TrimSpace(body.ArtifactID)
	}
	if body.ArtifactID == "" || len(body.State) == 0 || len(body.State) > maxArtifactStateBytes {
		writeErr(w, http.StatusBadRequest, "artifactId and state (<=16KiB) required")
		return
	}
	art, err := s.Artifacts.GetArtifact(r.Context(), body.ArtifactID)
	if err != nil || art.RoomID != roomID {
		writeErr(w, http.StatusNotFound, "artifact not found")
		return
	}
	if s.Publisher == nil {
		obs.ServerError(w, "realtime unavailable", nil)
		writeErr(w, http.StatusServiceUnavailable, "realtime unavailable")
		return
	}
	var state any
	_ = json.Unmarshal(body.State, &state)
	// Keep the last state as the join snapshot: a member who starts the
	// preview later (or refreshes) fetches it instead of a blank board.
	s.liveStates.set(body.ArtifactID, artifactStateSnapshot{State: state, Actor: userID, AtUnixMs: s.Now()})
	// Room-wide delivery, same rationale as artifact_updated — and stronger:
	// live state is transient, so a channel-filtered miss cannot be recovered
	// by any later poll. A member running the artifact from any channel must
	// receive every gesture.
	s.Publisher.Publish(r.Context(), wire.Event{
		RoomID: roomID,
		Kind:   "artifact_state",
		Payload: map[string]any{
			"artifactId": body.ArtifactID,
			"actor":      userID,
			"state":      state,
		},
		AtUnixMs: s.Now(),
	})
	w.WriteHeader(http.StatusOK)
}

// handleGetArtifactState returns the artifact's last relayed live state — the
// join snapshot for a member starting or refreshing a preview. Live state is
// session data, not document history: there is no backlog, only "where the
// board is right now". Authorization mirrors the relay (room membership, and
// the artifact must live in that room).
func (s *Service) handleGetArtifactState(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	userID := caller(r)
	if !s.Store.IsMember(r.Context(), roomID, userID) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	art, err := s.Artifacts.GetArtifact(r.Context(), artifactID)
	if err != nil || art.RoomID != roomID {
		writeErr(w, http.StatusNotFound, "artifact not found")
		return
	}
	snap, ok := s.liveStates.get(artifactID)
	if !ok {
		// No live session yet: an empty board, not an error.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}
