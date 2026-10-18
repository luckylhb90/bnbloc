package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// handleLiveFrame answers POST /v1/live: a frame of a job's live view, from
// a room-credential tool through the room relay (?job=&room=, the job
// grant: the job is live on the caller's bridge). The frame goes to the
// room's realtime owner, which holds the newest one and tells the room
// (wire/job_live.go); its answer (watching, control, queued input) is the
// tool's.
//
// After the turn (§3.7) a post is admitted only while the view waits for a
// person (frame.Waiting) or to end it (frame.End), from the bridge that
// ran the job last, for a requester who may still post in the room: the
// view a sign-in waits on stays while the person signs in. Realtime keeps
// it only while such posts go on (liveIdleFor).
func (s *Service) handleLiveFrame(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if strings.TrimSpace(r.URL.Query().Get("job")) == "" {
		httpx.Error(w, "a live frame is posted for a job (?job=&room=, the room relay sets them)", http.StatusBadRequest)
		return
	}
	var frame component.LiveFramePost
	// JSON carries the image base64: a third larger than the bytes.
	if !httpx.DecodeJSON(w, r, int64(component.MaxLiveFrameBytes)*4/3+64<<10, &frame) {
		return
	}
	job, err := s.liveJob(r, claims, frame.Waiting != "" || frame.End)
	if err != nil {
		writeGrantErr(w, err)
		return
	}
	if s.Backends.Realtime == "" {
		httpx.Unavailable(w, "live views")
		return
	}
	body, _ := json.Marshal(wire.LiveFrameIngest{ChannelID: job.ChannelID, Owner: job.OwnerUserID, Requester: job.RequesterUserID, Core: job.Core, Provider: job.Provider, Frame: frame})
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

// liveJob admits a live post's job: the job grant (contentGrant) while the
// job runs; once it ended, when outlive holds (the view waits for a person,
// or the post ends it), the job whose last lease was the caller's bridge
// (store.LoadRanJob) and whose requester may still post in the room.
func (s *Service) liveJob(r *http.Request, claims auth.Claims, outlive bool) (store.AgentJob, error) {
	actor, err := s.contentGrant(r, claims)
	var ge *grantError
	if err == nil {
		return *actor.Job, nil
	}
	if !outlive || !errors.As(err, &ge) || ge.status != http.StatusConflict || s.ContentJobs == nil {
		return store.AgentJob{}, err
	}
	q := r.URL.Query()
	roomID, jobID := strings.TrimSpace(q.Get("room")), strings.TrimSpace(q.Get("job"))
	scoped, ok := s.roomClaims(claims, roomID)
	if !ok {
		return store.AgentJob{}, refuseGrant(http.StatusForbidden, "room scope not authorized")
	}
	ctx := tenant.NewContext(r.Context(), scoped.Tenant)
	job, err := store.LoadRanJob(ctx, s.ContentJobs, jobID, roomID, claims.Subject)
	switch status := store.LeasedJobStatus(err); {
	case err == nil:
	case errors.Is(err, store.ErrJobNotInRoom):
		return store.AgentJob{}, refuseGrant(status, "job not found")
	case errors.Is(err, store.ErrJobNotLive):
		return store.AgentJob{}, refuseGrant(status, "job is not running")
	case errors.Is(err, store.ErrJobNotCallers):
		return store.AgentJob{}, refuseGrant(status, "job did not run on your bridge")
	default:
		return store.AgentJob{}, &grantError{status: status, msg: "load agent job failed", cause: err}
	}
	if job.TenantID == "" {
		job.TenantID = scoped.Tenant
	}
	if m, ok := s.roomMember(job.RequesterUserID, roomID); job.RequesterUserID == "" || !ok || !m.MayChat() {
		return store.AgentJob{}, refuseGrant(http.StatusForbidden, "the job's requester may no longer post in the room")
	}
	return job, nil
}
