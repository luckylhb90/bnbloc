package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// handleLiveFrame answers POST /v1/live: a frame of a job's live view, from
// a room-credential tool through the room relay (?job=&room=, the job
// grant: the job is live on the caller's bridge). The frame goes to the
// room's realtime owner, which holds the newest one and tells the room
// (wire/job_live.go); its answer (watching, control, queued input) is the
// tool's.
func (s *Service) handleLiveFrame(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if strings.TrimSpace(r.URL.Query().Get("job")) == "" {
		httpx.Error(w, "a live frame is posted for a job (?job=&room=, the room relay sets them)", http.StatusBadRequest)
		return
	}
	actor, err := s.contentGrant(r, claims)
	if err != nil {
		writeGrantErr(w, err)
		return
	}
	var frame wire.LiveFramePost
	// JSON carries the image base64: a third larger than the bytes.
	if !httpx.DecodeJSON(w, r, int64(wire.MaxLiveFrameBytes)*4/3+64<<10, &frame) {
		return
	}
	if s.Backends.Realtime == "" {
		httpx.Unavailable(w, "live views")
		return
	}
	job := actor.Job
	body, _ := json.Marshal(wire.LiveFrameIngest{ChannelID: job.ChannelID, Owner: job.OwnerUserID, Requester: job.RequesterUserID, Frame: frame})
	target := strings.TrimRight(s.Backends.Realtime, "/") + "/internal/v1/rooms/" + url.PathEscape(job.RoomID) + "/live/" + url.PathEscape(job.JobID)
	resp, err := callBackend(r.Context(), http.MethodPost, target, body, gatewayServiceSubject, job.TenantID)
	if err != nil {
		obs.ServerError(w, "hand a live frame to realtime failed", err)
		httpx.Error(w, "live views are unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e httpx.ErrorBody
		if json.Unmarshal(raw, &e) != nil || e.Error == "" {
			e.Error = "live views refused the frame"
		}
		httpx.WriteStatus(w, resp.StatusCode, e.Error, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
