package realtimehttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// handleInternalAppEvents is GET /internal/v1/rooms/{roomId}/app-events,
// the gateway's source for interactive apps' change feeds
// (docs/prd/2026-10-12-interactive-apps.md §5.1): the room's live
// app_changed events and nothing else from the room, each re-encoded as
// wire.AppChanged (metadata only). The gateway narrows it to one app and
// re-checks the app's capability (services/gateway/apps.go); only the
// gateway calls it (svcauth.RequireService), so no member is checked here.
// It is live only (no replay: a reconnecting feed re-fetches), keeps the
// connection open with heartbeats and ends when the room is deleted. The
// path names the room so the request reaches the room's owner replica.
func (s *Service) handleInternalAppEvents(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		obs.ServerError(w, "streaming unsupported", nil)
		httpx.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	sub := s.Hub.SubscribeLive(roomID)
	defer s.Hub.Unsubscribe(roomID, sub)
	defer realtime.TrackSSE(s.Hub.Metrics, realtime.SSEKindApp)()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(s.heartbeatEvery())
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case ev, open := <-sub.Events():
			if !open || ev.Kind == wire.EventRoomDeleted {
				return
			}
			change, ok := appChangeOf(ev)
			if !ok {
				continue
			}
			data, err := json.Marshal(change)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", wire.EventAppChanged, data)
			flusher.Flush()
		}
	}
}

// appChangeOf is an app_changed event's payload; ok=false for every other
// kind and for a payload that is not one.
func appChangeOf(ev wire.Event) (wire.AppChanged, bool) {
	if ev.Kind != wire.EventAppChanged {
		return wire.AppChanged{}, false
	}
	c, ok := wire.DecodePayload[wire.AppChanged](ev.Payload)
	// The cause is the room's own record (no self-trigger, room_events.go),
	// never an app's: its change feed reaches visitors of a public link.
	c.SourceJobID = ""
	return c, ok && c.AppID != ""
}
