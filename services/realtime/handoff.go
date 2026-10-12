package realtimehttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Handoff package support (F4). The room service orchestrates export/import
// (it owns the room, its members, channels and artifacts); realtime owns the
// feed and its sequence, so it serves the two feed-side primitives:
//
//   - GET  /internal/v1/rooms/{roomId}/feed   — the whole persisted feed as a
//     forward cursor (every channel, ascending seq, soft-deleted excluded).
//   - POST /internal/v1/rooms/{roomId}/import — history-only writes: each row
//     takes the room's next hub sequence and is persisted, but it is never
//     published, so no bridge drives an agent from it, no UI stream replays it,
//     no agent job is queued, and no plugin hook fires.
//
// Both are internal (svcauth.RequireSigned) AND check that the asserted user is
// a member of the room: the room service forwards the end user's identity.

const (
	// feedPageDefault / feedPageMax bound one feed page.
	feedPageDefault = 200
	feedPageMax     = 500
)

// handleInternalFeed returns one ascending page of the room's persisted feed
// with sequence > after.
func (s *Service) handleInternalFeed(w http.ResponseWriter, r *http.Request) {
	roomID, ok := s.handoffRoom(w, r)
	if !ok {
		return
	}
	if s.Chat == nil {
		writeFeedPage(w, nil)
		return
	}
	limit := int(parseInt(r.URL.Query().Get("limit")))
	if limit <= 0 {
		limit = feedPageDefault
	}
	if limit > feedPageMax {
		limit = feedPageMax
	}
	msgs, err := s.Chat.RoomFeed(r.Context(), roomID, parseInt(r.URL.Query().Get("after")), limit)
	if err != nil {
		obs.ServerError(w, "feed read failed", err)
		httpx.Error(w, "feed read failed", http.StatusInternalServerError)
		return
	}
	writeFeedPage(w, msgs)
}

func writeFeedPage(w http.ResponseWriter, msgs []store.RoomMessage) {
	page := wire.FeedPage{Messages: make([]wire.FeedMessage, 0, len(msgs))}
	for _, m := range msgs {
		page.Messages = append(page.Messages, wire.FeedMessage{
			Sequence:   m.Seq,
			ChannelID:  m.ChannelID,
			Kind:       m.Kind,
			Payload:    json.RawMessage(m.Payload),
			AtUnixMs:   m.CreatedAtMs,
			EditedAtMs: m.EditedAtMs,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

// handleInternalImport persists a batch of imported history rows into the
// room, in order, each on the room's next hub sequence. A row that fails
// validation rejects the whole batch before anything is written.
func (s *Service) handleInternalImport(w http.ResponseWriter, r *http.Request) {
	roomID, ok := s.handoffRoom(w, r)
	if !ok {
		return
	}
	if s.Chat == nil {
		httpx.Unavailable(w, "history store")
		return
	}
	var batch wire.RoomImportBatch
	if !httpx.DecodeJSON(w, r, wire.MaxImportBatchBytes, &batch) {
		return
	}
	if len(batch.Messages) > wire.MaxImportBatchMessages {
		httpx.Error(w, fmt.Sprintf("too many messages in one batch (max %d)", wire.MaxImportBatchMessages), http.StatusRequestEntityTooLarge)
		return
	}
	if strings.TrimSpace(batch.FromRoomID) == "" {
		httpx.Error(w, "fromRoomId is required", http.StatusBadRequest)
		return
	}
	importedAt := batch.ImportedAtMs
	if importedAt <= 0 {
		importedAt = time.Now().UnixMilli()
	}
	rows := make([]store.RoomMessage, 0, len(batch.Messages))
	for i, m := range batch.Messages {
		row, err := s.importRow(r, roomID, batch, importedAt, m)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errUnknownChannel) {
				status = http.StatusNotFound
			}
			httpx.WriteStatus(w, status, fmt.Sprintf("message %d: %v", i, err), err)
			return
		}
		rows = append(rows, row)
	}
	reply := wire.RoomImportBatchReply{}
	for _, row := range rows {
		row.TenantID = tenant.FromContext(r.Context())
		ev, err := s.Hub.CommitHistory(wire.Event{RoomID: roomID, ChannelID: row.ChannelID, Kind: row.Kind, AtUnixMs: row.CreatedAtMs},
			realtime.Durable{Journal: s.Chat, Row: row})
		if err != nil {
			obs.ServerError(w, "import persistence failed", err)
			httpx.Error(w, "import persistence failed", http.StatusInternalServerError)
			return
		}
		reply.Imported++
		reply.LastSequence = ev.Sequence
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

// importRow validates one imported message and builds its stored row. The
// payload keeps the original attribution (chat userId, agent ownerUserId +
// provider) and gains the server-authored payload.imported marker. The row's
// author column stays empty: author is the edit/delete authorization key, and
// an identity read from an uploaded file never authorizes anything.
func (s *Service) importRow(r *http.Request, roomID string, batch wire.RoomImportBatch, importedAt int64, m wire.FeedMessage) (store.RoomMessage, error) {
	if !wire.PersistedFeedKind(m.Kind) {
		return store.RoomMessage{}, fmt.Errorf("unsupported kind %q", m.Kind)
	}
	if len(m.Payload) > maxInternalPublishBytes {
		return store.RoomMessage{}, errors.New("payload too large")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(m.Payload, &payload); err != nil || payload == nil {
		return store.RoomMessage{}, errors.New("payload must be a JSON object")
	}
	if m.Kind == wire.KindChatMessage {
		var text string
		if raw, ok := payload["text"]; ok {
			if err := json.Unmarshal(raw, &text); err != nil {
				return store.RoomMessage{}, errors.New("chat text must be a string")
			}
		}
		if len(text) > maxChatTextBytes {
			return store.RoomMessage{}, errors.New("message too long")
		}
	}
	channelID, err := s.resolveChannel(r.Context(), roomID, m.ChannelID)
	if err != nil {
		return store.RoomMessage{}, err
	}
	marker, err := json.Marshal(wire.ImportedMarker{
		FromRoomID:       batch.FromRoomID,
		FromRoomName:     batch.FromRoomName,
		OriginalSequence: m.Sequence,
		ImportedAtMs:     importedAt,
		EditedAtMs:       m.EditedAtMs,
	})
	if err != nil {
		return store.RoomMessage{}, err
	}
	payload["imported"] = marker
	out, err := json.Marshal(payload)
	if err != nil {
		return store.RoomMessage{}, err
	}
	// The original timestamp is kept verbatim (0 = the source never knew it);
	// the import time lives in the marker, never in place of the original.
	return store.RoomMessage{
		RoomID:      roomID,
		ChannelID:   channelID,
		Kind:        m.Kind,
		Payload:     out,
		CreatedAtMs: max(m.AtUnixMs, 0),
	}, nil
}

// handoffRoom resolves the room and enforces membership of the asserted user
// for the internal handoff routes.
func (s *Service) handoffRoom(w http.ResponseWriter, r *http.Request) (string, bool) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	// A handoff exports or imports the whole feed, the people chat included:
	// a person's action, never an agent host's.
	if _, ok := s.member(w, r, roomID, userID, membership.Human); !ok {
		return "", false
	}
	return roomID, true
}
