// Package room implements the bnbloc-room HTTP service: room directory,
// membership, and invite lifecycle. roomId is the canonical workspace scope.
package room

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Invite time-to-live bounds: default when the caller omits an expiry, and a
// hard cap on any caller-supplied expiry.
const (
	defaultInviteTTLMs = 7 * 24 * 3600 * 1000
	maxInviteTTLMs     = 30 * 24 * 3600 * 1000
)

// Service holds the room handler dependencies.
type Service struct {
	Store store.RoomStore
	// AgentPrefs persists per-user, per-room+channel agent selections so the
	// composer restores the last choice on any device. Optional: nil disables
	// the endpoints (tests / minimal deployments).
	AgentPrefs store.AgentPrefsStore
	// Channels persists the room's message-stream channels.
	Channels store.ChannelStore
	// Artifacts persists the room's collaboration deliverables (specs, reports,
	// code) that members and agents preview and iterate on together.
	Artifacts store.ArtifactStore
	// Handoff reaches realtime's side of a handoff package (feed, attachment
	// blobs, history-only import). Nil disables export/import.
	Handoff HandoffFeed
	// Blobs is realtime's content-addressed blob store, which holds blob
	// artifact versions and thumbnails (realtime is the single owner of blob
	// storage; the room service reaches it over the signed internal route).
	// Nil answers blob publishes, blob content and thumbnails with 503.
	Blobs ArtifactBlobs
	// ArtifactMaxBytes caps one blob artifact version
	// (BNBLOC_ARTIFACT_MAX_BYTES); <= 0 means DefaultArtifactMaxBytes.
	ArtifactMaxBytes int64
	// RoomImports records the provenance of rooms created by an import.
	RoomImports store.RoomImportStore
	// Templates is the tenant room-template library (N3); Schedules lets a
	// template capture a room's schedules and create them (disabled) in a
	// new room. Nil Templates answers the template routes 503.
	Templates store.RoomTemplateStore
	Schedules store.RoomScheduleStore
	// Tenants is the tenant registry: the org policy switches (public share
	// links) and org roles (an org admin may revoke any share link). Nil
	// answers the share routes 503.
	Tenants store.TenantStore
	// Profiles holds users' display names (GET/PUT /v1/my/profile, GET
	// /v1/users/profiles, and the share resolve's "shared by"). Nil answers
	// those routes 503.
	Profiles store.UserProfileStore
	// Publisher fans artifact change events to the room's live subscribers.
	// Optional: nil (tests) simply skips the live notification.
	Publisher realtime.Publisher
	// Now returns the current unix-ms; injectable for tests.
	Now func() int64
	// liveStates holds the last relayed live state per artifact so a member
	// starting a preview joins the session in progress instead of an empty
	// board. Transient by design: artifact versions are the durable record,
	// and a restart simply empties the live boards.
	liveStates liveArtifactStates
}

// artifactStateSnapshot is one artifact's last known live state.
type artifactStateSnapshot struct {
	State    any    `json:"state"`
	Actor    string `json:"actor"`
	AtUnixMs int64  `json:"atUnixMs"`
}

// liveArtifactStates bounds the join-snapshot cache: at most 1024 artifacts
// (each payload already capped at 16KiB at the relay), evicting the oldest
// when full so a churning daemon cannot grow it without limit.
const maxLiveArtifactStates = 1024

type liveArtifactStates struct {
	mu sync.RWMutex
	m  map[string]artifactStateSnapshot
}

func (l *liveArtifactStates) set(id string, v artifactStateSnapshot) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = make(map[string]artifactStateSnapshot)
	}
	if len(l.m) >= maxLiveArtifactStates {
		var oldestKey string
		oldest := int64(math.MaxInt64)
		for k, e := range l.m {
			if e.AtUnixMs < oldest {
				oldest, oldestKey = e.AtUnixMs, k
			}
		}
		if oldestKey != "" {
			delete(l.m, oldestKey)
		}
	}
	l.m[id] = v
}

func (l *liveArtifactStates) get(id string) (artifactStateSnapshot, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	v, ok := l.m[id]
	return v, ok
}

// New constructs a room Service with a default clock.
func New(s store.RoomStore, ch store.ChannelStore) *Service {
	return &Service{Store: s, Channels: ch, Now: func() int64 { return time.Now().UnixMilli() }}
}

// Handler builds the HTTP mux for the room service.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/rooms", s.handleCreateRoom)
	mux.HandleFunc("GET /v1/rooms", s.handlePlaza)
	mux.HandleFunc("GET /v1/rooms/{roomId}", s.handleGetRoom)
	mux.HandleFunc("PATCH /v1/rooms/{roomId}", s.handlePatchRoom)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}", s.handleDeleteRoom)
	mux.HandleFunc("POST /v1/rooms/{roomId}/join", s.handleJoinRoom)
	mux.HandleFunc("POST /v1/rooms/{roomId}/leave", s.handleLeaveRoom)
	mux.HandleFunc("POST /v1/rooms/{roomId}/transfer", s.handleTransferRoom)
	mux.HandleFunc("POST /v1/rooms/{roomId}/members", s.handleAddMember)
	mux.HandleFunc("GET /v1/rooms/{roomId}/members", s.handleListMembers)
	mux.HandleFunc("GET /v1/rooms/{roomId}/export", s.handleExportRoom)
	mux.HandleFunc("POST /v1/rooms/import", s.handleImportRoom)
	mux.HandleFunc("GET /v1/rooms/{roomId}/handoff", s.handleGetHandoff)
	mux.HandleFunc("GET /v1/rooms/{roomId}/artifacts", s.handleListArtifacts)
	mux.HandleFunc("GET /v1/rooms/{roomId}/artifact-anchors", s.handleArtifactAnchors)
	mux.HandleFunc("POST /v1/rooms/{roomId}/artifacts", s.handleCreateArtifact)
	mux.HandleFunc("GET /v1/rooms/{roomId}/artifacts/{artifactId}", s.handleGetArtifact)
	mux.HandleFunc("POST /v1/rooms/{roomId}/artifacts/{artifactId}/versions", s.handlePublishVersion)
	mux.HandleFunc("GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}", s.handleGetArtifactVersion)
	mux.HandleFunc("GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/content", s.handleArtifactContent)
	mux.HandleFunc("GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/thumbnail", s.handleGetArtifactThumbnail)
	mux.HandleFunc("PUT /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/thumbnail", s.handlePutArtifactThumbnail)
	mux.HandleFunc("POST /v1/rooms/{roomId}/artifacts/{artifactId}/state", s.handleArtifactState)
	mux.HandleFunc("GET /v1/rooms/{roomId}/artifacts/{artifactId}/state", s.handleGetArtifactState)
	mux.HandleFunc("GET /v1/rooms/{roomId}/channels", s.handleListChannels)
	mux.HandleFunc("POST /v1/rooms/{roomId}/channels", s.handleCreateChannel)
	mux.HandleFunc("PATCH /v1/rooms/{roomId}/channels/{channelId}", s.handleRenameChannel)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/channels/{channelId}", s.handleDeleteChannel)
	mux.HandleFunc("GET /v1/rooms/{roomId}/people-chat", s.handleGetPeopleChat)
	mux.HandleFunc("POST /v1/rooms/{roomId}/people-chat", s.handlePeopleChat)
	mux.HandleFunc("GET /v1/users/{userId}/rooms", s.handleUserRooms)
	mux.HandleFunc("GET /v1/rooms/{roomId}/agent-prefs", s.handleGetAgentPrefs)
	mux.HandleFunc("PUT /v1/rooms/{roomId}/agent-prefs", s.handlePutAgentPrefs)
	mux.HandleFunc("GET /v1/rooms/{roomId}/members/{userId}", s.handleGetMember)
	mux.HandleFunc("GET /v1/rooms/{roomId}/membership/{userId}", s.handleMembershipProbe)
	mux.HandleFunc("GET /v1/rooms/{roomId}/tenant", s.handleRoomTenant)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/members/{userId}", s.handleRemoveMember)
	mux.HandleFunc("POST /v1/rooms/{roomId}/invites", s.handleCreateInvite)
	mux.HandleFunc("POST /v1/invites/{code}/redeem", s.handleRedeem)
	s.registerTemplateRoutes(mux)
	s.registerShareRoutes(mux)
	s.registerProfileRoutes(mux)
	s.registerAgentHostRoutes(mux)
	return svcauth.EnsureTenant(mux)
}

func (s *Service) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	caller := caller(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	var body struct {
		RoomID      string `json:"roomId"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Visibility  string `json:"visibility"`
		SyncMode    string `json:"syncMode"`
	}
	if !decode(w, r, &body) {
		return
	}
	roomID, err := roomctx.Normalize(body.RoomID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The id becomes a path segment everywhere (gateway routing, backend mux,
	// bridge URLs) and a VARCHAR(64) key; refuse what cannot be stored/routed.
	if err := roomctx.ValidateID(roomID); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid room id: 1-64 bytes, no whitespace edges, control characters, or / \\ ? # %")
		return
	}
	room := store.Room{
		RoomID:      roomID,
		Name:        body.Name,
		Description: body.Description,
		Visibility:  store.NormalizeVisibility(body.Visibility),
		SyncMode:    store.NormalizeSyncMode(body.SyncMode),
		CreatedAtMs: s.Now(),
	}
	if _, err := s.createRoom(r.Context(), room, caller, ""); err != nil {
		// An existing id (any tenant, including a deleted room) is a conflict,
		// never an adoption: creating must not hand the caller someone else's
		// room (BE-1).
		if errors.Is(err, store.ErrRoomExists) {
			writeErr(w, http.StatusConflict, "room id already exists")
			return
		}
		obs.ServerError(w, "create room failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roomId": roomID, "visibility": string(room.Visibility), "syncMode": string(room.SyncMode)})
}

// handleGetRoom returns full detail to members. To a non-member it returns a
// minimal preview for a shared room (so a join card can render) and 404 for a
// private room (do not confirm existence beyond the plaza's locked row).
func (s *Service) handleGetRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	rm, err := s.Store.GetRoom(r.Context(), roomID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	isMember := s.Store.IsMember(r.Context(), roomID, caller(r))
	if !isMember {
		if rm.Visibility != store.VisibilityShared {
			writeErr(w, http.StatusNotFound, "room not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"roomId": rm.RoomID, "name": rm.Name, "description": rm.Description,
			"visibility": string(rm.Visibility), "memberCount": s.Store.MemberCount(r.Context(), roomID),
			"preview": true,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"roomId": rm.RoomID, "name": rm.Name, "description": rm.Description,
		"visibility": string(rm.Visibility), "syncMode": string(rm.SyncMode), "createdBy": rm.CreatedBy,
		"createdAtMs": rm.CreatedAtMs, "memberCount": s.Store.MemberCount(r.Context(), roomID),
	})
}

func (s *Service) handleAddMember(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	role, ok := s.Store.MemberRole(r.Context(), roomID, caller(r))
	if !ok || !role.CanInvite() {
		writeErr(w, http.StatusForbidden, "caller may not add members")
		return
	}
	var body struct {
		UserID string `json:"userId"`
		Role   string `json:"role"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.addMember(r.Context(), roomID, body.UserID, normalizeRole(body.Role)); err != nil {
		writeMemberErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleListMembers returns every membership in a room. The caller must itself
// be a member. This backs the members panel, presence roster, @-mention list,
// and the borrow owner picker.
func (s *Service) handleListMembers(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.Store.IsMember(r.Context(), roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	memberships := s.Store.Members(r.Context(), roomID)
	members := make([]map[string]any, 0, len(memberships))
	for _, m := range memberships {
		// An agent host (a platform agent's host, docs/prd/2026-10-10-zero-
		// install-onboarding.md §2) is listed only with its flag: clients
		// keep it out of people lists.
		members = append(members, map[string]any{
			"userId":    m.UserID,
			"role":      string(m.Role),
			"agentHost": m.AgentHost,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Service) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	target := r.PathValue("userId")
	role, ok := s.Store.MemberRole(r.Context(), roomID, caller(r))
	if !ok || !role.CanInvite() {
		writeErr(w, http.StatusForbidden, "caller may not remove members")
		return
	}
	// Removing yourself is the Leave flow, not a kick; keep the semantics clean.
	if strings.EqualFold(target, caller(r)) {
		writeErr(w, http.StatusBadRequest, "use leave to remove yourself")
		return
	}
	// The owner cannot be kicked; ownership must be transferred first.
	if targetRole, ok := s.Store.MemberRole(r.Context(), roomID, target); ok && targetRole == store.RoleOwner {
		writeErr(w, http.StatusForbidden, "cannot remove the room owner; transfer ownership first")
		return
	}
	if err := s.Store.RemoveMember(r.Context(), roomID, target); err != nil {
		obs.ServerError(w, "remove member failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publishMemberRemoved(r, roomID, target)
	w.WriteHeader(http.StatusOK)
}

// publishMemberRemoved tells realtime a member's access to the room ended, so
// it closes that member's live streams now instead of letting them keep
// receiving room events until their next reconnect (BE-10).
func (s *Service) publishMemberRemoved(r *http.Request, roomID, userID string) {
	if s.Publisher == nil {
		return
	}
	s.Publisher.Publish(r.Context(), wire.Event{
		RoomID:   roomID,
		Kind:     wire.EventMemberRemoved,
		Payload:  wire.MemberRemovedPayload{UserID: userID},
		AtUnixMs: s.Now(),
	})
}

// handleGetMember is the membership probe consumed by RoomClient. Returns 200
// with the role when the target user is a member, 404 otherwise. The caller
// must itself be a member to query.
func (s *Service) handleGetMember(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.Store.IsMember(r.Context(), roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	m, ok := s.Store.GetMember(r.Context(), roomID, r.PathValue("userId"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not a member")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"userId": m.UserID, "role": string(m.Role), "agentHost": m.AgentHost})
}

// handleMembershipProbe answers "is userId a member of roomId in ANY tenant",
// used by the gateway room-scope fallback for a multi-tenant member whose room
// lives outside their token's tenant. It is tenant-agnostic by design (room ids
// are globally unique), so unlike handleGetMember it does not gate on the
// caller's own tenant-scoped membership — the signed X-Bnbloc-User (verified by
// svcauth middleware) and the requested subject are the authorization.
//
// It answers {member, role, agentHost}: an agent-host membership is a member
// (the host's bridge reads the room) that every human-only surface refuses
// (membership.RoomClient.IsHumanMember).
func (s *Service) handleMembershipProbe(w http.ResponseWriter, r *http.Request) {
	m, member := s.Store.MembershipAnyTenant(r.Context(), r.PathValue("roomId"), r.PathValue("userId"))
	if !member {
		writeJSON(w, http.StatusOK, map[string]any{"member": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": true, "role": string(m.Role), "agentHost": m.AgentHost})
}

// isHumanMember reports whether user is a member of the room whose
// membership is not an agent host's.
func (s *Service) isHumanMember(r *http.Request, roomID, user string) bool {
	m, ok := s.Store.GetMember(r.Context(), roomID, user)
	return ok && !m.AgentHost
}

// handleRoomTenant returns a room's authoritative tenant, resolved tenant-
// agnostically by room_id (the PK). The gateway calls it to derive the tenant a
// room-scoped request must execute in, before it knows that tenant. Not gated on
// caller membership: the tenant id is not sensitive (it is an org routing key,
// not org data), and the signed svcauth header already authenticates the caller;
// the room-scoped operation that follows is still membership-gated downstream.
func (s *Service) handleRoomTenant(w http.ResponseWriter, r *http.Request) {
	tid, ok := s.Store.RoomTenant(r.Context(), r.PathValue("roomId"))
	if !ok {
		writeErr(w, http.StatusNotFound, "room not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"tenantId": tid})
}

// handleUserRooms returns the room ids a user is a member of. The gateway calls
// this at login to stamp room memberships into the session token. A caller may
// only query its own memberships.
func (s *Service) handleUserRooms(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")
	if caller(r) != userID {
		writeErr(w, http.StatusForbidden, "caller may only query own rooms")
		return
	}
	// The bridge discovery shape: every room the owner is a member of across
	// all tenants. The bridge subscribes/advertises/claims in each of them, so
	// a session token minted in one tenant must not hide the owner's rooms in
	// another. UI listings keep the default tenant-scoped shape. Agent-host
	// memberships (a platform agent's host) are the user's rooms only for the
	// bridge, which asks with includeAgentHost=1: they are appended to rooms
	// and also listed in agentHostRooms, so the bridge serves the hosted
	// agent there without treating the room as its owner's own.
	if r.URL.Query().Get("allTenants") == "1" {
		rooms := s.Store.RoomsForUserAllTenants(r.Context(), userID)
		if r.URL.Query().Get("includeAgentHost") != "1" {
			writeJSON(w, http.StatusOK, map[string][]string{"rooms": rooms})
			return
		}
		hosted := s.Store.AgentHostRoomsAllTenants(r.Context(), userID)
		if hosted == nil {
			hosted = []string{}
		}
		writeJSON(w, http.StatusOK, map[string][]string{"rooms": append(rooms, hosted...), "agentHostRooms": hosted})
		return
	}
	if r.URL.Query().Get("detail") == "" {
		// Login path: bare id list used to stamp token room scope.
		writeJSON(w, http.StatusOK, map[string][]string{"rooms": s.Store.RoomsForUser(r.Context(), userID)})
		return
	}
	details := s.Store.RoomsForUserDetailed(r.Context(), userID)
	rooms := make([]map[string]any, 0, len(details))
	for _, d := range details {
		rooms = append(rooms, map[string]any{
			"roomId": d.RoomID, "name": d.Name, "description": d.Description,
			"visibility": string(d.Visibility), "syncMode": string(d.SyncMode), "role": string(d.Role),
			"isOwner": d.IsOwner, "memberCount": d.MemberCount, "createdAtMs": d.CreatedAtMs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": rooms})
}

func (s *Service) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	role, ok := s.Store.MemberRole(r.Context(), roomID, caller(r))
	if !ok || !role.CanInvite() {
		writeErr(w, http.StatusForbidden, "caller may not create invites")
		return
	}
	var body struct {
		Role        string `json:"role"`
		ExpiresAtMs int64  `json:"expiresAtMs"`
		Capacity    int    `json:"capacity"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Capacity < 1 {
		writeErr(w, http.StatusBadRequest, "capacity must be >= 1")
		return
	}
	now := s.Now()
	if body.ExpiresAtMs <= 0 {
		body.ExpiresAtMs = now + defaultInviteTTLMs
	} else if body.ExpiresAtMs > now+maxInviteTTLMs {
		body.ExpiresAtMs = now + maxInviteTTLMs
	}
	code := randomCode()
	inv := store.Invite{Code: code, RoomID: roomID, Role: normalizeInviteRole(body.Role), ExpiresAtMs: body.ExpiresAtMs, Capacity: body.Capacity, CreatedBy: caller(r)}
	if err := s.Store.CreateInvite(r.Context(), inv); err != nil {
		obs.ServerError(w, "create invite failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "roomId": roomID, "role": string(inv.Role), "expiresAtMs": inv.ExpiresAtMs, "capacity": inv.Capacity})
}

func (s *Service) handleRedeem(w http.ResponseWriter, r *http.Request) {
	caller := caller(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	m, err := s.Store.RedeemInvite(r.Context(), r.PathValue("code"), caller, s.Now())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInviteNotFound):
			writeErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, store.ErrInviteExpired), errors.Is(err, store.ErrInviteFull):
			writeErr(w, http.StatusForbidden, err.Error())
		default:
			obs.ServerError(w, "redeem invite failed", err)
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roomId": m.RoomID, "membership": map[string]string{"userId": m.UserID, "role": string(m.Role)}})
}

// handlePlaza lists rooms for discovery. Private rooms are returned locked with
// only non-sensitive fields; shared rooms include member counts and are joinable.
func (s *Service) handlePlaza(w http.ResponseWriter, r *http.Request) {
	if caller(r) == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), 50)
	offset := atoiDefault(q.Get("offset"), 0)
	rooms, total, err := s.Store.SearchRooms(r.Context(), q.Get("q"), limit, offset)
	if err != nil {
		obs.ServerError(w, "list plaza rooms failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(rooms))
	for _, rc := range rooms {
		if rc.Visibility == store.VisibilityShared {
			out = append(out, map[string]any{
				"roomId": rc.RoomID, "name": rc.Name, "description": rc.Description,
				"visibility": "shared", "syncMode": string(rc.SyncMode), "createdBy": rc.CreatedBy, "createdAtMs": rc.CreatedAtMs,
				"memberCount": rc.MemberCount, "locked": false,
			})
			continue
		}
		// Private: redact everything but the existence + name/description.
		out = append(out, map[string]any{
			"roomId": rc.RoomID, "name": rc.Name, "description": rc.Description,
			"visibility": "private", "locked": true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": out, "total": total, "limit": limit, "offset": offset})
}

// handleJoinRoom self-adds the caller to a shared room. Private rooms refuse
// (an invite code is required), returning 403.
func (s *Service) handleJoinRoom(w http.ResponseWriter, r *http.Request) {
	caller := caller(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	roomID := r.PathValue("roomId")
	rm, err := s.Store.GetRoom(r.Context(), roomID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	// An agent-host membership is not one: joining makes the host a human
	// member (AddMember converts the row).
	if m, ok := s.Store.GetMember(r.Context(), roomID, caller); ok && !m.AgentHost {
		writeJSON(w, http.StatusOK, map[string]any{"roomId": roomID, "role": string(m.Role)})
		return
	}
	if rm.Visibility != store.VisibilityShared {
		writeErr(w, http.StatusForbidden, "room is private; an invite code is required")
		return
	}
	if err := s.Store.AddMember(r.Context(), store.Membership{RoomID: roomID, UserID: caller, Role: store.RoleMember, JoinedAtMs: s.Now()}); err != nil {
		obs.ServerError(w, "join room failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roomId": roomID, "role": string(store.RoleMember)})
}

// handleLeaveRoom removes the caller's membership. The owner cannot leave; they
// must transfer ownership or delete the room first.
func (s *Service) handleLeaveRoom(w http.ResponseWriter, r *http.Request) {
	caller := caller(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	roomID := r.PathValue("roomId")
	role, ok := s.Store.MemberRole(r.Context(), roomID, caller)
	if !ok {
		writeErr(w, http.StatusNotFound, "not a member")
		return
	}
	if role == store.RoleOwner {
		writeErr(w, http.StatusConflict, "owner cannot leave; transfer ownership or delete the room")
		return
	}
	if err := s.Store.RemoveMember(r.Context(), roomID, caller); err != nil {
		obs.ServerError(w, "leave room failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publishMemberRemoved(r, roomID, caller)
	w.WriteHeader(http.StatusOK)
}

// handleTransferRoom transfers ownership to another member (owner-only).
func (s *Service) handleTransferRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if role, ok := s.Store.MemberRole(r.Context(), roomID, caller(r)); !ok || role != store.RoleOwner {
		writeErr(w, http.StatusForbidden, "only the owner may transfer ownership")
		return
	}
	var body struct {
		UserID string `json:"userId"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.UserID) == "" {
		writeErr(w, http.StatusBadRequest, "userId is required")
		return
	}
	if err := s.Store.TransferOwner(r.Context(), roomID, body.UserID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roomId": roomID, "owner": body.UserID})
}

// handlePatchRoom updates name/description/visibility (owner-only).
func (s *Service) handlePatchRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if role, ok := s.Store.MemberRole(r.Context(), roomID, caller(r)); !ok || role != store.RoleOwner {
		writeErr(w, http.StatusForbidden, "only the owner may edit the room")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Visibility  *string `json:"visibility"`
		SyncMode    *string `json:"syncMode"`
	}
	if !decode(w, r, &body) {
		return
	}
	patch := store.RoomPatch{Name: body.Name, Description: body.Description}
	if body.Visibility != nil {
		v := store.NormalizeVisibility(*body.Visibility)
		patch.Visibility = &v
	}
	if body.SyncMode != nil {
		m := store.NormalizeSyncMode(*body.SyncMode)
		patch.SyncMode = &m
	}
	if err := s.Store.UpdateRoom(r.Context(), roomID, patch); err != nil {
		if errors.Is(err, store.ErrRoomNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		obs.ServerError(w, "patch room failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	rm, _ := s.Store.GetRoom(r.Context(), roomID)
	writeJSON(w, http.StatusOK, map[string]any{
		"roomId": rm.RoomID, "name": rm.Name, "description": rm.Description, "visibility": string(rm.Visibility), "syncMode": string(rm.SyncMode),
	})
}

// handleDeleteRoom soft-deletes a room (owner-only).
func (s *Service) handleDeleteRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if role, ok := s.Store.MemberRole(r.Context(), roomID, caller(r)); !ok || role != store.RoleOwner {
		writeErr(w, http.StatusForbidden, "only the owner may delete the room")
		return
	}
	if err := s.Store.DeleteRoom(r.Context(), roomID); err != nil {
		if errors.Is(err, store.ErrRoomNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		obs.ServerError(w, "delete room failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Every member's live stream closes: the room no longer exists (BE-10).
	if s.Publisher != nil {
		s.Publisher.Publish(r.Context(), wire.Event{RoomID: roomID, Kind: wire.EventRoomDeleted, AtUnixMs: s.Now()})
	}
	w.WriteHeader(http.StatusOK)
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func normalizeRole(v string) store.Role {
	switch store.Role(strings.TrimSpace(v)) {
	case store.RoleOwner:
		return store.RoleOwner
	case store.RoleAdmin:
		return store.RoleAdmin
	case store.RoleVisitor:
		return store.RoleVisitor
	default:
		return store.RoleMember
	}
}

func normalizeInviteRole(v string) store.Role {
	if store.Role(strings.TrimSpace(v)) == store.RoleVisitor {
		return store.RoleVisitor
	}
	return store.RoleMember
}

func randomCode() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func caller(r *http.Request) string { return strings.TrimSpace(r.Header.Get("X-Bnbloc-User")) }

// maxJSONBodyBytes caps the small JSON control bodies (room/member/invite/
// channel create and patch) decode accepts, so a client cannot stream an
// unbounded payload into the decoder (BE-17). Artifact bodies carry their own
// larger caps.
const maxJSONBodyBytes = 64 << 10

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// handleGetAgentPrefs returns the caller's last-selected agent for a room+
// channel context (falling back is the client's job: room-level row, then the
// user's global default). Members only, like every room-scoped read.
func (s *Service) handleGetAgentPrefs(w http.ResponseWriter, r *http.Request) {
	if s.AgentPrefs == nil {
		obs.ServerError(w, "agent prefs not configured", nil)
		writeErr(w, http.StatusNotImplemented, "agent prefs not configured")
		return
	}
	roomID := r.PathValue("roomId")
	user := caller(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	if !s.Store.IsMember(r.Context(), roomID, user) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	channel := r.URL.Query().Get("channel")
	p, ok := s.AgentPrefs.GetAgentPrefs(r.Context(), roomID, channel, user)
	if !ok {
		p = store.AgentPrefs{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"roomId": roomID, "channelId": channel,
		"core": p.Core, "provider": p.Provider, "model": p.Model, "reasoning": p.Reasoning,
		"found": ok,
	})
}

// handlePutAgentPrefs upserts the caller's selection for a room+channel.
// Silently tolerates a body missing optional fields; the client debounces.
func (s *Service) handlePutAgentPrefs(w http.ResponseWriter, r *http.Request) {
	if s.AgentPrefs == nil {
		obs.ServerError(w, "agent prefs not configured", nil)
		writeErr(w, http.StatusNotImplemented, "agent prefs not configured")
		return
	}
	roomID := r.PathValue("roomId")
	user := caller(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	if !s.Store.IsMember(r.Context(), roomID, user) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	var body struct {
		Channel   string `json:"channelId"`
		Core      string `json:"core"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(body.Provider) == "" {
		writeErr(w, http.StatusBadRequest, "provider is required")
		return
	}
	if err := s.AgentPrefs.PutAgentPrefs(r.Context(), store.AgentPrefs{
		RoomID: roomID, ChannelID: body.Channel,
		Core: body.Core, Provider: body.Provider, Model: body.Model, Reasoning: body.Reasoning,
		UpdatedAtMs: s.Now(),
	}, user); err != nil {
		obs.ServerError(w, "put agent prefs failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
