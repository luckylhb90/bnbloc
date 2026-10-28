package room

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
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
	// EditedFrom is the version an edit request started from (0 = none).
	EditedFrom int `json:"editedFrom"`
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
	isMember, err := s.Store.IsMember(r.Context(), roomID, userID)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !isMember {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	var body createArtifactBody
	if !decodeArtifactBody(w, r, &body) {
		return
	}
	body.Kind = strings.TrimSpace(body.Kind)
	body.Title = strings.TrimSpace(body.Title)
	if !validArtifactKind(body.Kind) {
		httpx.Error(w, "kind must be one of markdown, code, image, html, app, file, site", http.StatusBadRequest)
		return
	}
	if body.Kind == store.ArtifactKindSite && body.Content != "" {
		httpx.Error(w, errSiteNeedsSnapshot, http.StatusBadRequest)
		return
	}
	if body.Title == "" {
		httpx.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	if len([]rune(body.Title)) > maxArtifactTitleRunes {
		httpx.Error(w, "title is too long", http.StatusBadRequest)
		return
	}
	if len(body.Content) > maxArtifactContentBytes {
		httpx.Error(w, "content exceeds the 2 MiB limit", http.StatusRequestEntityTooLarge)
		return
	}
	body.FileName = sanitizeArtifactFileName(body.FileName)
	if len([]rune(body.FileName)) > maxArtifactFileNameRunes {
		httpx.Error(w, "fileName is too long", http.StatusBadRequest)
		return
	}
	now := s.Now()
	id := ids.New("art_")
	// A caller may carry its own stable id (the bridge derives one per session
	// file, so an agent's turns publish versions of ONE artifact instead of a
	// duplicate per turn). Server-minted when absent; shape-validated when
	// present, and the store's idempotent put keeps any existing history.
	if callerID := strings.TrimSpace(body.ArtifactId); callerID != "" {
		if len(callerID) < 8 || len(callerID) > 64 || !artifactIDShape.MatchString(callerID) {
			httpx.Error(w, "invalid artifactId", http.StatusBadRequest)
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
	if errors.Is(err, store.ErrArtifactIDTaken) {
		httpx.Error(w, "artifactId is taken by another room", http.StatusConflict)
		return
	}
	if err != nil {
		obs.ServerError(w, "create artifact failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
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
			httpx.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		art.CurrentVersion = v.Version
		art.UpdatedAtMs = v.CreatedAtMs
		art.MimeType = v.MimeType
	}
	s.publishArtifactEvent(r.Context(), art, userID, "")
	httpx.WriteJSON(w, http.StatusOK, wire.ArtifactReply{Artifact: art.View()})
}

// handleListArtifacts lists the room's artifacts, newest-updated first, with
// filters (channelId, kind, mime — exact or "type/*" —, author, sourceSeq, q)
// and keyset paging (limit ≤ 200, default 100; nextCursor → cursor). maxBytes
// is the blob version cap, so publishers skip files the room would refuse.
func (s *Service) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.requireRead(w, r, roomID) {
		return
	}
	q, ok := parseArtifactQuery(w, r, roomID)
	if !ok {
		return
	}
	readable, ok := s.artifactChannels(w, r, roomID)
	if !ok {
		return
	}
	arts, err := s.Artifacts.ListArtifacts(r.Context(), q)
	if err != nil {
		obs.ServerError(w, "list artifacts failed", err)
		httpx.Error(w, "list artifacts failed", http.StatusInternalServerError)
		return
	}
	// The cursor follows the page as read, so paging still advances past a
	// page whose artifacts are all of channels the reader may not read.
	full := len(arts) == q.Limit
	var next string
	if full {
		next = artifactCursor(arts[len(arts)-1])
	}
	shown := arts[:0:0]
	for _, a := range arts {
		if readable(a.ChannelID) {
			shown = append(shown, a)
		}
	}
	resp := wire.ArtifactList{Artifacts: store.ArtifactViews(shown), MaxBytes: s.artifactMaxBytes()}
	if full {
		resp.NextCursor = next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// handleArtifactAnchors answers which artifacts were produced by the room
// messages with seq in [from, to]: GET .../artifact-anchors?from=&to= →
// {anchors: [{seq, artifactId}]}. The feed anchors its deliverable cards
// under exactly the messages it has loaded, however old.
func (s *Service) handleArtifactAnchors(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.requireRead(w, r, roomID) {
		return
	}
	from, errFrom := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, errTo := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if errFrom != nil || errTo != nil || from < 1 || to < from {
		httpx.Error(w, "from and to must be sequences with 1 <= from <= to", http.StatusBadRequest)
		return
	}
	anchors, err := s.Artifacts.ArtifactAnchors(r.Context(), roomID, from, to)
	if err != nil {
		obs.ServerError(w, "list artifact anchors failed", err)
		httpx.Error(w, "list artifact anchors failed", http.StatusInternalServerError)
		return
	}
	// An anchor names a message of a channel; the reader sees it only when
	// they read the artifact (roomArtifact's rule).
	readable, ok := s.artifactChannels(w, r, roomID)
	if !ok {
		return
	}
	shown := anchors[:0:0]
	for _, a := range anchors {
		if art, err := s.Artifacts.GetArtifact(r.Context(), a.ArtifactID); err == nil && readable(art.ChannelID) {
			shown = append(shown, a)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"anchors": shown})
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
		// app=1: interactive apps only (store.Artifact.IsApp).
		Apps:  v.Get("app") == "1",
		Limit: 100,
	}
	if raw := strings.TrimSpace(v.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 200 {
			httpx.Error(w, "limit must be 1..200", http.StatusBadRequest)
			return q, false
		}
		q.Limit = n
	}
	if raw := strings.TrimSpace(v.Get("sourceSeq")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			httpx.Error(w, "invalid sourceSeq", http.StatusBadRequest)
			return q, false
		}
		q.SourceSeq = n
	}
	before, id, ok := parseArtifactCursor(w, v.Get("cursor"))
	q.BeforeUpdatedMs, q.BeforeID = before, id
	return q, ok
}

// artifactCursor is the keyset cursor after the last artifact of a page
// (newest-updated first): "<updatedAtMs>:<artifactId>".
func artifactCursor(a store.Artifact) string {
	return strconv.FormatInt(a.UpdatedAtMs, 10) + ":" + a.ArtifactID
}

// parseArtifactCursor reads an artifactCursor ("" is the first page); it
// answers 400 and ok=false on a malformed one.
func parseArtifactCursor(w http.ResponseWriter, raw string) (updatedMs int64, id string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, "", true
	}
	ms, id, cut := strings.Cut(raw, ":")
	n, err := strconv.ParseInt(ms, 10, 64)
	if !cut || err != nil || n <= 0 || id == "" {
		httpx.Error(w, "invalid cursor", http.StatusBadRequest)
		return 0, "", false
	}
	return n, id, true
}

func (s *Service) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.requireRead(w, r, roomID) {
		return
	}
	art, ok := s.roomArtifact(w, r, roomID, r.PathValue("artifactId"))
	if !ok {
		return
	}
	versions, err := s.Artifacts.ListArtifactVersions(r.Context(), art.ArtifactID, 50)
	if err != nil {
		obs.ServerError(w, "list artifact versions failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.ArtifactDetail{Artifact: art.View(), Versions: store.ArtifactVersionViews(versions)})
}

func (s *Service) handleGetArtifactVersion(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.requireRead(w, r, roomID) {
		return
	}
	art, ok := s.roomArtifact(w, r, roomID, r.PathValue("artifactId"))
	if !ok {
		return
	}
	version, perr := strconv.Atoi(strings.TrimSpace(r.PathValue("version")))
	if perr != nil {
		httpx.Error(w, "invalid version", http.StatusBadRequest)
		return
	}
	v, err := s.Artifacts.GetArtifactVersion(r.Context(), art.ArtifactID, version)
	if err != nil {
		httpx.Error(w, "version not found", http.StatusNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.ArtifactVersionReply{Version: v.View()})
}

func (s *Service) handlePublishVersion(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	userID := caller(r)
	isMember, err := s.Store.IsMember(r.Context(), roomID, userID)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !isMember {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	art, ok := s.roomArtifact(w, r, roomID, r.PathValue("artifactId"))
	if !ok {
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		s.publishBlobVersion(w, r, art, userID)
		return
	}
	if art.Kind == store.ArtifactKindSite {
		httpx.Error(w, errSiteNeedsSnapshot, http.StatusBadRequest)
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
		httpx.Error(w, "content exceeds the 2 MiB limit", http.StatusRequestEntityTooLarge)
		return
	}
	if body.BaseVersion < 0 {
		httpx.Error(w, "baseVersion must be >= 0", http.StatusBadRequest)
		return
	}
	if !validEditedFrom(w, body.EditedFrom, body.BaseVersion) {
		return
	}
	s.commitVersion(w, r, art, userID, store.ArtifactVersion{
		Content: body.Content, MimeType: store.ArtifactInlineMIME(art),
		SourceJobID: strings.TrimSpace(body.SourceJobID), SourceSeq: body.SourceSeq,
		Summary: strings.TrimSpace(body.Summary), AuthorUserID: userID, CreatedAtMs: s.Now(),
		EditedFrom: body.EditedFrom,
	}, body.BaseVersion)
}

// validEditedFrom checks an edit's start version: none (0), or a version
// the artifact has (at most the base the publish builds on).
func validEditedFrom(w http.ResponseWriter, editedFrom, baseVersion int) bool {
	if editedFrom < 0 || editedFrom > baseVersion {
		httpx.Error(w, "editedFrom must name an existing version (at most baseVersion)", http.StatusBadRequest)
		return false
	}
	return true
}

// validVersionProvenance bounds a version's summary and source link.
func validVersionProvenance(w http.ResponseWriter, summary, sourceJobID string, sourceSeq int64) bool {
	if len([]rune(strings.TrimSpace(summary))) > maxArtifactSummaryRunes {
		httpx.Error(w, "summary is too long", http.StatusBadRequest)
		return false
	}
	if len(strings.TrimSpace(sourceJobID)) > maxArtifactSourceJobLen || sourceSeq < 0 {
		httpx.Error(w, "invalid sourceJobId or sourceSeq", http.StatusBadRequest)
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
			httpx.Error(w, "artifact was updated concurrently", http.StatusConflict)
			return
		}
		obs.ServerError(w, "publish version failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishArtifactEvent(r.Context(), art, userID, v.SourceJobID)
	view := v.View()
	httpx.WriteJSON(w, http.StatusOK, wire.ArtifactReply{Artifact: art.View(), Version: &view})
}

// authorizeArtifactRead checks the caller may read the room (a member, or
// anyone in a shared room) and that the artifact belongs to
// the room in the path, so an id leaked across rooms cannot be probed.
func (s *Service) authorizeArtifactRead(w http.ResponseWriter, r *http.Request, roomID string) bool {
	if !s.requireRead(w, r, roomID) {
		return false
	}
	_, ok := s.roomArtifact(w, r, roomID, r.PathValue("artifactId"))
	return ok
}

// roomArtifact is the ONE read of an artifact of the room for a request:
// the artifact, when it is the room's and its channel is one the request's
// reader may read (C13: a deliverable of a members-only channel is read
// only by those who read the channel); 404 otherwise, as for no such
// artifact. The caller has checked the room itself.
func (s *Service) roomArtifact(w http.ResponseWriter, r *http.Request, roomID, artifactID string) (store.Artifact, bool) {
	art, err := s.Artifacts.GetArtifact(r.Context(), artifactID)
	if err != nil || art.RoomID != roomID {
		httpx.Error(w, "artifact not found", http.StatusNotFound)
		return store.Artifact{}, false
	}
	readable, ok := s.artifactChannels(w, r, roomID)
	if !ok {
		return store.Artifact{}, false
	}
	if !readable(art.ChannelID) {
		httpx.Error(w, "artifact not found", http.StatusNotFound)
		return store.Artifact{}, false
	}
	return art, true
}

// artifactChannels answers which channels' artifacts the request's reader
// may read (MayReadChannel): an artifact of no channel ("") or of a channel
// that is gone is the room's (read by anyone in it).
func (s *Service) artifactChannels(w http.ResponseWriter, r *http.Request, roomID string) (func(channelID string) bool, bool) {
	access, ok := s.readerAccess(w, r, roomID)
	if !ok {
		return nil, false
	}
	if !access.HasMembersOnly() {
		return func(string) bool { return true }, true
	}
	channels, err := s.Channels.ListChannels(r.Context(), roomID)
	if err != nil {
		obs.ServerError(w, "list channels failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	barred := map[string]bool{}
	for _, c := range channels {
		if !access.MayReadChannel(c) {
			barred[c.ChannelID] = true
		}
	}
	return func(channelID string) bool { return !barred[channelID] }, true
}

// publishArtifactEvent tells the room's subscribers (web panels, bridges) that
// an artifact changed, so previews refresh live. Metadata only: the SSE stream
// never carries content. sourceJob is the agent job whose turn published the
// new version ("" for a person's own): the room's event (room.file.added)
// carries it as its cause.
func (s *Service) publishArtifactEvent(ctx context.Context, art store.Artifact, actor, sourceJob string) {
	s.publishArtifactEventReason(ctx, art, actor, "", sourceJob)
}

// publishArtifactEventReason is publishArtifactEvent with a reason for a
// change that is not a new version ("thumbnail": a version thumbnail was
// stored, so file cards refetch it).
func (s *Service) publishArtifactEventReason(ctx context.Context, art store.Artifact, actor, reason, sourceJob string) {
	if reason == "" {
		s.pulseArtifactVersion(art, actor)
	}
	if s.Publisher == nil {
		return
	}
	payload := wire.ArtifactUpdated{
		ArtifactID:     art.ArtifactID,
		Kind:           art.Kind,
		Title:          art.Title,
		MimeType:       art.MimeType,
		FileName:       art.FileName,
		CurrentVersion: art.CurrentVersion,
		UpdatedBy:      actor,
		UpdatedAtMs:    art.UpdatedAtMs,
		Reason:         reason,
		SourceJobID:    sourceJob,
	}
	// Artifact events reach every stream of the room (the Docs panel is a
	// room-scoped surface: a viewer in another channel must still see a
	// version land), yet name the artifact's channel: realtime delivers them
	// across channels (wire.CrossChannelKind) to the readers of that channel
	// only (C13: a members-only channel's deliverable is its readers').
	s.Publisher.Publish(ctx, wire.Event{
		RoomID:    art.RoomID,
		ChannelID: art.ChannelID,
		Kind:      wire.EventArtifactUpdated,
		Payload:   payload,
		AtUnixMs:  art.UpdatedAtMs,
	})
}

func decodeArtifactBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !httpx.DecodeJSON(w, r, maxArtifactContentBytes+(64<<10), dst) {
		return false
	}
	return true
}

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
	isMember, err := s.Store.IsMember(r.Context(), roomID, userID)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !isMember {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	var body artifactStateBody
	if !httpx.DecodeJSON(w, r, wire.MaxArtifactStateBytes+(4<<10), &body) {
		return
	}
	// The path carries the artifact id (the client already knows it there); a
	// body artifactId is accepted for symmetry but the path wins.
	if pathID := strings.TrimSpace(r.PathValue("artifactId")); pathID != "" {
		body.ArtifactID = pathID
	} else {
		body.ArtifactID = strings.TrimSpace(body.ArtifactID)
	}
	if body.ArtifactID == "" || len(body.State) == 0 || len(body.State) > wire.MaxArtifactStateBytes {
		httpx.Error(w, "artifactId and state (<=16KiB) required", http.StatusBadRequest)
		return
	}
	art, ok := s.roomArtifact(w, r, roomID, body.ArtifactID)
	if !ok {
		return
	}
	if s.Publisher == nil {
		obs.ServerError(w, "realtime unavailable", nil)
		httpx.Error(w, "realtime unavailable", http.StatusServiceUnavailable)
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
		RoomID:    roomID,
		ChannelID: art.ChannelID,
		Kind:      wire.EventArtifactState,
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
// board is right now". Authorization is the room's read gate (preview.go: a
// member, or anyone in a shared room), and the artifact must live in that
// room.
func (s *Service) handleGetArtifactState(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.requireRead(w, r, roomID) {
		return
	}
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if _, ok := s.roomArtifact(w, r, roomID, artifactID); !ok {
		return
	}
	snap, ok := s.liveStates.get(artifactID)
	if !ok {
		// No live session yet: an empty board, not an error.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, snap)
}
