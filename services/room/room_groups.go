package room

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Group chats connected to a room (docs/prd/2026-10-15-app-identity-
// schedules-notify-ai.md §5a). The room service owns them: they are the
// room's, set by its members, deleted with it.
//
//	GET    /v1/rooms/{roomId}/groups                  members: the connected group chats
//	POST   /v1/rooms/{roomId}/groups/connect          members who may act: a connect code for a name
//	DELETE /v1/rooms/{roomId}/groups/{name}           members who may act: disconnect
//	POST   /internal/v1/group-binds?notifier=         the gateway (a verified notifier): spend a code, connect the chat
//
// A notice's "group:<name>" resolves through noticeGroup (appnotify.go).

// codeAlphabet spells connect codes: no 0/O or 1/I, read off a phone.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newGroupCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return wire.RoomGroupCodePrefix + string(b)
}

// validGroupName is a group's name: 1 to wire.MaxNotifyGroupLen printable
// characters, no ":" (it ends the "group:" prefix nowhere else).
func validGroupName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= wire.MaxNotifyGroupLen &&
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || r == ':' }) < 0
}

func (s *Service) registerRoomGroupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/rooms/{roomId}/groups", s.handleListRoomGroups)
	mux.HandleFunc("POST /v1/rooms/{roomId}/groups/connect", s.handleConnectRoomGroup)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/groups/{name}", s.handleDeleteRoomGroup)
	mux.Handle("POST /internal/v1/group-binds", svcauth.RequireService(svcauth.KeyFromEnv(), []string{"gateway"}, http.HandlerFunc(s.handleBindRoomGroup)))
}

// roomGroupsMember checks the caller is a member (mayAct: one who may act
// in the room, not a visitor).
func (s *Service) roomGroupsMember(w http.ResponseWriter, r *http.Request, mayAct bool) (string, bool) {
	if s.RoomGroups == nil {
		httpx.Unavailable(w, "room group chats")
		return "", false
	}
	roomID := r.PathValue("roomId")
	m, ok, err := s.Store.GetMember(r.Context(), roomID, caller(r))
	switch {
	case err != nil:
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, "read membership failed", http.StatusInternalServerError)
		return "", false
	case !ok || m.AgentHost:
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return "", false
	case mayAct && !(membership.Member{Role: m.Role}).MayChat():
		httpx.Error(w, "visitors cannot change the room's group chats", http.StatusForbidden)
		return "", false
	}
	return roomID, true
}

func (s *Service) handleListRoomGroups(w http.ResponseWriter, r *http.Request) {
	roomID, ok := s.roomGroupsMember(w, r, false)
	if !ok {
		return
	}
	list, err := s.RoomGroups.ListRoomGroups(r.Context(), roomID)
	if err != nil {
		obs.ServerError(w, "list room groups failed", err)
		httpx.Error(w, "list room groups failed", http.StatusInternalServerError)
		return
	}
	out := wire.RoomGroupsReply{Groups: make([]wire.RoomGroup, 0, len(list))}
	for _, g := range list {
		out.Groups = append(out.Groups, wire.RoomGroup{Name: g.Name, Notifier: g.Notifier, ConnectedBy: g.ConnectedBy, CreatedAtMs: g.CreatedAtMs})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleConnectRoomGroup(w http.ResponseWriter, r *http.Request) {
	roomID, ok := s.roomGroupsMember(w, r, true)
	if !ok {
		return
	}
	var in wire.RoomGroupConnectRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if !validGroupName(name) {
		httpx.Error(w, fmt.Sprintf("a group's name is 1 to %d characters, without ':'", wire.MaxNotifyGroupLen), http.StatusBadRequest)
		return
	}
	ctx, now := r.Context(), s.Now()
	list, err := s.RoomGroups.ListRoomGroups(ctx, roomID)
	if err != nil {
		obs.ServerError(w, "list room groups failed", err)
		httpx.Error(w, "list room groups failed", http.StatusInternalServerError)
		return
	}
	exists := false
	for _, g := range list {
		exists = exists || g.Name == name
	}
	if !exists && len(list) >= wire.MaxRoomGroups {
		httpx.Error(w, fmt.Sprintf("a room connects at most %d group chats", wire.MaxRoomGroups), http.StatusConflict)
		return
	}
	open, err := s.RoomGroups.OpenRoomGroupCodes(ctx, roomID, now)
	if err != nil {
		obs.ServerError(w, "count connect codes failed", err)
		httpx.Error(w, "count connect codes failed", http.StatusInternalServerError)
		return
	}
	if open >= wire.MaxRoomGroupCodesOpen {
		httpx.Error(w, "too many open connect codes: use one, or wait ten minutes", http.StatusTooManyRequests)
		return
	}
	c := store.RoomGroupCode{Code: newGroupCode(), RoomID: roomID, Name: name, CreatedBy: caller(r), ExpiresAtMs: now + wire.RoomGroupCodeTTLMs}
	if err := s.RoomGroups.PutRoomGroupCode(ctx, c); err != nil {
		obs.ServerError(w, "store connect code failed", err)
		httpx.Error(w, "store connect code failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, wire.RoomGroupConnect{Name: name, Code: c.Code, ExpiresAtMs: c.ExpiresAtMs})
}

func (s *Service) handleDeleteRoomGroup(w http.ResponseWriter, r *http.Request) {
	roomID, ok := s.roomGroupsMember(w, r, true)
	if !ok {
		return
	}
	err := s.RoomGroups.DeleteRoomGroup(r.Context(), roomID, r.PathValue("name"))
	switch {
	case errors.Is(err, store.ErrRoomGroupNotFound):
		httpx.Error(w, "no such group chat in this room", http.StatusNotFound)
	case err != nil:
		obs.ServerError(w, "disconnect room group failed", err)
		httpx.Error(w, "disconnect room group failed", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleBindRoomGroup spends a connect code a notifier saw in a group chat
// (the gateway verified the notifier's signature) and connects the chat.
func (s *Service) handleBindRoomGroup(w http.ResponseWriter, r *http.Request) {
	if s.RoomGroups == nil {
		httpx.Unavailable(w, "room group chats")
		return
	}
	notifier := strings.TrimSpace(r.URL.Query().Get("notifier"))
	var in wire.NotifierGroupBindRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &in) {
		return
	}
	code, address := strings.ToUpper(strings.TrimSpace(in.Code)), strings.TrimSpace(in.Address)
	code = wire.RoomGroupCodePrefix + strings.TrimPrefix(code, strings.ToUpper(wire.RoomGroupCodePrefix))
	if notifier == "" || address == "" || len(address) > wire.MaxGroupAddressLen {
		httpx.Error(w, "a bind needs the notifier, the code and the chat's address", http.StatusBadRequest)
		return
	}
	g, err := s.RoomGroups.BindRoomGroup(r.Context(), code, notifier, address, s.Now())
	switch {
	case errors.Is(err, store.ErrRoomGroupCode):
		httpx.Error(w, "this code is unknown or expired: ask for a new one in bnbloc", http.StatusNotFound)
		return
	case err != nil:
		obs.ServerError(w, "bind room group failed", err)
		httpx.Error(w, "bind room group failed", http.StatusInternalServerError)
		return
	}
	out := wire.NotifierGroupBindReply{Name: g.Name}
	if room, err := s.Store.GetRoom(tenant.NewContext(r.Context(), g.TenantID), g.RoomID); err == nil {
		out.RoomName = room.Name
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
