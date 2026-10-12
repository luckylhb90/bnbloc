package realtimehttp

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Team features served by realtime, which owns the room feed they build on:
//   - F6 message tasks: /v1/rooms/{roomId}/tasks (any room member), announced
//     live by the transient task_updated event.
//   - F7 global search: /v1/search across the caller's rooms, resolved
//     server-side from bnbloc-room — never from the request.

// Task and search input bounds.
const (
	maxTaskTitleRunes  = 200
	maxSearchLimit     = 50
	defaultSearchLimit = 20
)

// RoomDirectory lists the rooms a member belongs to (bnbloc-room is the
// authority). Global search reads only rooms it returns.
type RoomDirectory interface {
	MemberRooms(ctx context.Context, userID, tenantID string) ([]membership.RoomRef, error)
}

func (s *Service) registerTeamRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/rooms/{roomId}/tasks", s.handleListTasks)
	mux.HandleFunc("PUT /v1/rooms/{roomId}/tasks/{seq}", s.handlePutTask)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/tasks/{seq}", s.handleDeleteTask)
	mux.HandleFunc("GET /v1/search", s.handleSearch)
	// Search reindex and status (super-admin, gated at the gateway).
	mux.HandleFunc("POST /v1/platform/search/reindex", s.handleSearchReindex)
	mux.HandleFunc("GET /v1/platform/search/status", s.handleSearchStatus)
}

// requireSubject returns the authenticated caller, answering 401 when absent.
func requireSubject(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := svcauth.Subject(r)
	if userID == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return "", false
	}
	return userID, true
}

// requireRoomMember resolves the {roomId} path value and the caller, failing
// closed (403) unless the caller's membership has the ability need
// (s.member).
func (s *Service) requireRoomMember(w http.ResponseWriter, r *http.Request, need membership.Need) (roomID, userID string, m membership.Member, ok bool) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return "", "", m, false
	}
	if userID, ok = requireSubject(w, r); !ok {
		return "", "", m, false
	}
	if m, ok = s.member(w, r, roomID, userID, need); !ok {
		return "", "", m, false
	}
	return roomID, userID, m, true
}

// member is realtime's one membership check of a request
// (membership.RoomScope): userID's membership of roomID in the request's
// tenant, with the ability need. It answers the 403 itself.
func (s *Service) member(w http.ResponseWriter, r *http.Request, roomID, userID string, need membership.Need) (membership.Member, bool) {
	m, err := membership.RoomScope(r.Context(), s.Membership, roomID, userID, need)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusForbidden)
		return m, false
	}
	return m, true
}

// ------------------------------------------------------------------ tasks

func (s *Service) tasksReady(w http.ResponseWriter) bool {
	if s.Tasks == nil || s.Chat == nil {
		obs.ServerError(w, "message tasks unavailable", nil)
		httpx.Error(w, "message tasks unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// taskTitle snapshots a message's first non-empty line as the task title.
func taskTitle(text string, seq int64) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return truncateRunes(line, maxTaskTitleRunes)
		}
	}
	return "Message #" + strconv.FormatInt(seq, 10)
}

func (s *Service) handleListTasks(w http.ResponseWriter, r *http.Request) {
	roomID, _, _, ok := s.requireRoomMember(w, r, membership.AnyMember)
	if !ok || !s.tasksReady(w) {
		return
	}
	tasks, err := s.Tasks.ListMessageTasks(r.Context(), roomID)
	if err != nil {
		obs.ServerError(w, "task read failed", err)
		httpx.Error(w, "task read failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

// handlePutTask marks a persisted message as a task or toggles its status
// ({"status":"open"|"done"}). Any room member may do either. The first mark
// snapshots the message's title, author and channel; later puts change only
// the status. The change is announced with a transient task_updated event.
func (s *Service) handlePutTask(w http.ResponseWriter, r *http.Request) {
	roomID, userID, _, ok := s.requireRoomMember(w, r, membership.AnyMember)
	if !ok || !s.tasksReady(w) {
		return
	}
	seq := parseInt(r.PathValue("seq"))
	if seq <= 0 {
		httpx.Error(w, "invalid message", http.StatusBadRequest)
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if !httpx.DecodeJSON(w, r, maxSmallBodyBytes, &body) {
		return
	}
	if body.Status != wire.TaskOpen && body.Status != wire.TaskDone {
		httpx.Error(w, "status must be open or done", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	now := time.Now().UnixMilli()
	task, found, err := s.Tasks.GetMessageTask(ctx, roomID, seq)
	if err != nil {
		obs.ServerError(w, "task read failed", err)
		httpx.Error(w, "task read failed", http.StatusInternalServerError)
		return
	}
	if !found {
		msg, exists, err := s.Chat.GetRoomMessage(ctx, roomID, seq)
		if err != nil {
			obs.ServerError(w, "message read failed", err)
			httpx.Error(w, "message read failed", http.StatusInternalServerError)
			return
		}
		if !exists {
			httpx.Error(w, "message not found", http.StatusNotFound)
			return
		}
		task = wire.MessageTask{
			RoomID:        roomID,
			ChannelID:     msg.ChannelID,
			Seq:           seq,
			Title:         taskTitle(store.MessageText(msg.Kind, msg.Payload), seq),
			MessageAuthor: store.MessageAuthor(msg),
			CreatedBy:     userID,
			CreatedAtMs:   now,
		}
	}
	task.Status = body.Status
	task.DoneBy, task.DoneAtMs = "", 0
	if body.Status == wire.TaskDone {
		task.DoneBy, task.DoneAtMs = userID, now
	}
	stored, err := s.Tasks.PutMessageTask(ctx, task)
	if err != nil {
		obs.ServerError(w, "task write failed", err)
		httpx.Error(w, "task write failed", http.StatusInternalServerError)
		return
	}
	s.publishTaskUpdated(roomID, stored.ChannelID, wire.TaskUpdatedPayload{Seq: seq, Task: &stored}, now)
	httpx.WriteJSON(w, http.StatusOK, stored)
}

// handleDeleteTask unmarks a message (any room member).
func (s *Service) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	roomID, _, _, ok := s.requireRoomMember(w, r, membership.AnyMember)
	if !ok || !s.tasksReady(w) {
		return
	}
	seq := parseInt(r.PathValue("seq"))
	if seq <= 0 {
		httpx.Error(w, "invalid message", http.StatusBadRequest)
		return
	}
	removed, err := s.Tasks.DeleteMessageTask(r.Context(), roomID, seq)
	if err != nil {
		obs.ServerError(w, "task write failed", err)
		httpx.Error(w, "task write failed", http.StatusInternalServerError)
		return
	}
	if !removed {
		httpx.Error(w, "message is not a task", http.StatusNotFound)
		return
	}
	s.publishTaskUpdated(roomID, "", wire.TaskUpdatedPayload{Seq: seq, Removed: true}, time.Now().UnixMilli())
	w.WriteHeader(http.StatusNoContent)
}

// publishTaskUpdated announces a task change (the web room stream receives
// every channel, so the room's task strip stays current). A put is scoped to
// the task message's channel — its title snapshots the message text, which
// must not reach a stream that may not see that channel (the people chat);
// a removal carries only the sequence and is room-wide (channelID "").
// Transient: persist() only stores chat_message and agent_messages, so it
// never enters history.
func (s *Service) publishTaskUpdated(roomID, channelID string, p wire.TaskUpdatedPayload, atMs int64) {
	s.Hub.Publish(wire.Event{RoomID: roomID, ChannelID: channelID, Kind: wire.EventTaskUpdated, Payload: p, AtUnixMs: atMs})
}

// ----------------------------------------------------------------- search

// searchChannelKind labels a message hit's channel kind ("people" for the
// people chat) so the client can show and open it as such; memo caches one
// lookup per (room, channel) per search. A failed lookup leaves the hit
// unlabeled — the client then opens it as an ordinary channel, which is a
// display fallback only (search results are the caller's own memberships).
func (s *Service) searchChannelKind(ctx context.Context, memo map[string]string, roomID, channelID string) string {
	if channelID == "" || s.Channels == nil {
		return ""
	}
	key := roomID + "\x00" + channelID
	if k, ok := memo[key]; ok {
		return k
	}
	kind, _, err := s.Channels.ChannelKind(ctx, roomID, channelID)
	if err != nil {
		log.Printf("realtime: search: channel kind room=%s channel=%s: %v", roomID, channelID, err)
	}
	memo[key] = kind
	return kind
}

// handleSearch is the global search: GET /v1/search?q=&rooms=&limit=&before=.
// The rooms searched are the caller's memberships as bnbloc-room reports them;
// the optional rooms param can only narrow that set. Messages page by the
// before cursor (created_at_ms of the oldest hit); artifact hits come with
// the first page only.
func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireSubject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	text := strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(text) < store.SearchMinRunes {
		httpx.Error(w, "query must be at least 2 characters", http.StatusBadRequest)
		return
	}
	limit := int(parseInt(q.Get("limit")))
	if limit <= 0 || limit > maxSearchLimit {
		limit = defaultSearchLimit
	}
	before := parseInt(q.Get("before"))
	ctx := r.Context()
	if s.Rooms == nil {
		obs.ServerError(w, "room directory unavailable", nil)
		httpx.Error(w, "room directory unavailable", http.StatusServiceUnavailable)
		return
	}
	member, err := s.Rooms.MemberRooms(ctx, userID, tenant.FromContext(ctx))
	if err != nil {
		obs.ServerError(w, "room directory unavailable", err)
		httpx.Error(w, "room directory unavailable", http.StatusServiceUnavailable)
		return
	}
	filter := map[string]bool{}
	for _, raw := range strings.Split(q.Get("rooms"), ",") {
		if id := strings.TrimSpace(raw); id != "" {
			filter[id] = true
		}
	}
	names := map[string]string{}
	var roomIDs []string
	for _, rm := range member {
		if len(filter) > 0 && !filter[rm.RoomID] {
			continue
		}
		names[rm.RoomID] = rm.Name
		roomIDs = append(roomIDs, rm.RoomID)
	}

	resp := wire.SearchResponse{Results: []wire.SearchResult{}}
	if s.Search != nil && len(roomIDs) > 0 {
		// The index sees only the rooms resolved above; whatever backend
		// answers, every hit is a store row that passed the same scope.
		msgs, err := s.Search.Search(ctx, store.MessageSearch{RoomIDs: roomIDs, Text: text, BeforeMs: before, Limit: limit})
		if err != nil {
			obs.ServerError(w, "search messages ("+s.Search.Backend()+")", err)
			httpx.Error(w, "search failed", http.StatusInternalServerError)
			return
		}
		kinds := map[string]string{} // room\x00channel -> channel kind
		for _, m := range msgs {
			resp.Results = append(resp.Results, wire.SearchResult{
				Kind:        wire.SearchMessage,
				RoomID:      m.RoomID,
				RoomName:    names[m.RoomID],
				ChannelID:   m.ChannelID,
				ChannelKind: s.searchChannelKind(ctx, kinds, m.RoomID, m.ChannelID),
				Seq:         m.Seq,
				Snippet:     snippetAround(store.MessageText(m.Kind, m.Payload), text),
				Author:      store.MessageAuthor(m),
				AtMs:        m.CreatedAtMs,
			})
		}
		if len(msgs) == limit {
			resp.NextBefore = msgs[len(msgs)-1].CreatedAtMs
		}
	}
	if before <= 0 && s.Artifacts != nil && len(roomIDs) > 0 {
		arts, err := s.Artifacts.SearchArtifacts(ctx, roomIDs, text, limit)
		if err != nil {
			log.Printf("realtime: search artifacts: %v", err)
		}
		for _, a := range arts {
			resp.Results = append(resp.Results, wire.SearchResult{
				Kind:         wire.SearchArtifact,
				RoomID:       a.RoomID,
				RoomName:     names[a.RoomID],
				ChannelID:    a.ChannelID,
				ArtifactID:   a.ArtifactID,
				ArtifactKind: a.Kind,
				MimeType:     a.MimeType,
				FileName:     a.FileName,
				Title:        a.Title,
				Snippet:      a.Title,
				Author:       a.CreatedBy,
				AtMs:         a.UpdatedAtMs,
			})
		}
	}
	// Prompts of the prompt packs the member can see belong to no room:
	// first page only, and only when no room filter is set.
	if before <= 0 && len(filter) == 0 && s.Content != nil {
		hits, err := contentuse.SearchPrompts(ctx, s.Content, extpipe.Viewer{Subject: userID, Tenant: tenant.FromContext(ctx)}, text, limit)
		if err != nil {
			log.Printf("realtime: search prompts: %v", err)
		}
		for _, h := range hits {
			resp.Results = append(resp.Results, wire.SearchResult{
				Kind:        wire.SearchPrompt,
				ComponentID: h.Component.ID,
				PromptID:    h.Prompt.ID,
				Title:       h.Prompt.Title,
				Snippet:     snippetAround(h.Prompt.Body, text),
				Author:      h.Component.Author(),
				AtMs:        h.Component.UpdatedAtMs,
			})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// snippetAround returns a short single-line excerpt of text centred on the
// first case-insensitive occurrence of query (the head of text when absent).
func snippetAround(text, query string) string {
	const before, after = 60, 120
	runes := []rune(strings.Join(strings.Fields(text), " "))
	q := []rune(query)
	at := -1
	for i := 0; i+len(q) <= len(runes); i++ {
		if strings.EqualFold(string(runes[i:i+len(q)]), query) {
			at = i
			break
		}
	}
	if at < 0 {
		return truncateRunes(string(runes), before+after)
	}
	start := max(0, at-before)
	end := min(len(runes), at+len(q)+after)
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// serverTimeIfUnset keeps a client-supplied event time and stamps the server's
// when it is missing, so every stored feed row has a usable created_at_ms
// (global search orders and pages by it).
func serverTimeIfUnset(atMs int64) int64 {
	if atMs > 0 {
		return atMs
	}
	return time.Now().UnixMilli()
}
