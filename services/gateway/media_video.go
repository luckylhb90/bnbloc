package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/media"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Video generation (docs/prd/2026-10-19-video-generation.md §2.2): a video
// takes from half a minute to minutes, so it is submitted and then polled,
// and the gateway keeps nothing. The id is the provider's own, so any
// replica answers for it; a done video is published into the job's room
// under an id derived from the room and the video's, so a repeated poll
// answers the same file. The admission, the provider, the refusals and the
// audit row are the image call's (media.go).

// handleMediaVideo answers POST /v1/media/video: one video submitted to the
// organization's provider, answered at once (rendering, with its id). The
// caller's assist rate and the cost budget are spent here, once per video;
// the audit row has the model and the latency, never the prompt.
func (s *Service) handleMediaVideo(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	// A room tool's call runs as the job's requester (?job=&room=, the
	// content routes' admission): the bridge driving the job acts for them.
	actor, ok := s.mediaCaller(w, r, claims)
	if !ok {
		return
	}
	claims = actor.Claims
	if !s.assistLimiter.Get(assistRate, assistBurst, s.replicaCount).Allow(claims.Tenant + "/" + claims.Subject) {
		httpx.Error(w, "too many requests: try again in a minute", http.StatusTooManyRequests)
		return
	}
	var req wire.MediaVideoRequest
	if !httpx.DecodeJSON(w, r, 64<<10, &req) {
		return
	}
	if req.Prompt, ok = mediaPrompt(w, req.Prompt, "video"); !ok {
		return
	}
	switch {
	case req.Aspect != "" && !slices.Contains(wire.MediaVideoAspects, req.Aspect):
		httpx.Error(w, "aspect must be one of "+strings.Join(wire.MediaVideoAspects, ", "), http.StatusBadRequest)
		return
	case req.Seconds < 0:
		httpx.Error(w, "seconds is the video's length (0: the provider's default)", http.StatusBadRequest)
		return
	}
	p, has, err := s.mediaProvider(ctx, claims, media.CapVideo, ownAuthOf(r, actor))
	if err != nil {
		obs.ServerError(w, "read the media provider failed", err)
		httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
		return
	}
	if !has {
		httpx.Error(w, errMediaUnavailable.Error(), http.StatusConflict)
		return
	}
	model := p.Choose(req.Model)
	start := time.Now()
	st, err := p.SubmitVideo(ctx, media.VideoRequest{Prompt: req.Prompt, Model: model, Seconds: req.Seconds, Aspect: req.Aspect})
	s.auditMediaRun(ctx, claims, "video", "", wire.AssistUsage{Model: model, LatencyMs: timeSinceMs(s, start)}, err)
	if err != nil {
		writeMediaErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.MediaVideoReply{ID: st.ID, Status: st.State, Model: firstNonEmpty(st.Model, model), Error: st.Error})
}

// handleMediaVideoStatus answers GET /v1/media/video/{id}: the video's
// status. Done, with ?publish=true under a job grant (the relay adds
// ?room=&job=), it is published into the job's room as the requester
// (?fileName=, an .mp4) and the reply names the file; a video already
// published there is answered without asking the provider again. Without a
// job it is answered done, never inlined. A poll spends no budget (the
// submit did); it is admitted like the submit. A failed video and a
// published one are audited (the final polls), never the prompt.
func (s *Service) handleMediaVideoStatus(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	actor, ok := s.mediaCaller(w, r, claims)
	if !ok {
		return
	}
	claims = actor.Claims
	id := r.PathValue("id")
	if !media.ValidVideoID(id) {
		httpx.Error(w, "the id is not a video's id", http.StatusBadRequest)
		return
	}
	var place *mediaPlace
	if r.URL.Query().Get("publish") == "true" {
		place = jobMediaPlace(actor.Job) // a member's own call publishes nothing
	}
	p, has, err := s.mediaProvider(ctx, claims, media.CapVideo, ownAuthOf(r, actor))
	if err != nil {
		obs.ServerError(w, "read the media provider failed", err)
		httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
		return
	}
	if !has {
		httpx.Error(w, errMediaUnavailable.Error(), http.StatusConflict)
		return
	}
	if place != nil {
		done, found, err := s.publishedVideo(ctx, claims, place.RoomID, id)
		if err != nil {
			obs.ServerError(w, "read the published video failed", err)
			httpx.Error(w, "reading the room's files failed: try again", http.StatusBadGateway)
			return
		}
		if found {
			httpx.WriteJSON(w, http.StatusOK, done)
			return
		}
	}
	st, err := p.VideoStatus(ctx, id)
	if err != nil {
		writeMediaErr(w, err)
		return
	}
	out := wire.MediaVideoReply{ID: id, Status: st.State, Model: st.Model, Error: st.Error}
	switch {
	case st.State == wire.MediaVideoFailed:
		s.auditMediaRun(ctx, claims, "video", "", wire.AssistUsage{Model: st.Model}, errVideoFailed)
	case st.State == wire.MediaVideoDone && place != nil:
		start := time.Now()
		file, err := s.publishVideo(ctx, claims, p, *place, id, r.URL.Query().Get("fileName"))
		s.auditMediaRun(ctx, claims, "video", " (published)", wire.AssistUsage{Model: st.Model, LatencyMs: timeSinceMs(s, start)}, err)
		switch {
		case errors.Is(err, errVideoPublish):
			obs.ServerError(w, "publish the generated video failed", err)
			httpx.Error(w, "the video is done, but publishing it into the room failed: try again", http.StatusBadGateway)
			return
		case err != nil:
			writeMediaErr(w, err)
			return
		}
		out.ArtifactID, out.FileName, out.Bytes = file.ArtifactID, file.FileName, file.Bytes
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// errVideoFailed: the provider answered the video failed (its reason is
// the reply's, never the audit's).
var errVideoFailed = errors.New("the provider failed the video")

// errVideoPublish: the video's file was read, and the room refused or
// failed its publish.
var errVideoPublish = errors.New("publishing the video into the room failed")

// publishVideo reads a done video's file, at most the room's file cap, and
// publishes it as the room file videoArtifactID names (publishMediaFile).
// A poll racing this one may publish it first: its first version is then
// refused (409), and the file it published is the answer.
func (s *Service) publishVideo(ctx context.Context, claims auth.Claims, p media.Provider, place mediaPlace, id, asked string) (wire.MediaVideoReply, error) {
	limit, err := s.roomFileCap(ctx, claims, place.RoomID)
	if err != nil {
		return wire.MediaVideoReply{}, fmt.Errorf("%w: %v", errVideoPublish, err)
	}
	data, mimeType, err := p.VideoContent(ctx, id, limit)
	if err != nil {
		return wire.MediaVideoReply{}, err
	}
	f := mediaFile{Name: videoFileName(asked), Bytes: data, MimeType: mimeType, ArtifactID: videoArtifactID(place.RoomID, id)}
	pub, err := s.publishMediaFile(ctx, claims, place, f, nil)
	var rbe *roomBackendError
	if errors.As(err, &rbe) && rbe.Status == http.StatusConflict {
		done, found, rerr := s.publishedVideo(ctx, claims, place.RoomID, id)
		if rerr == nil && found {
			return done, nil
		}
	}
	if err != nil {
		return wire.MediaVideoReply{}, fmt.Errorf("%w: %v", errVideoPublish, err)
	}
	return wire.MediaVideoReply{ID: id, Status: wire.MediaVideoDone, ArtifactID: pub.ArtifactID, FileName: f.Name, Bytes: int64(len(data))}, nil
}

// publishedVideo is the room file a done video became (videoArtifactID),
// read as the member in claims: found false when it was not published
// (yet).
func (s *Service) publishedVideo(ctx context.Context, claims auth.Claims, roomID, id string) (wire.MediaVideoReply, bool, error) {
	art, v, err := s.deliverableVersion(ctx, claims, roomID, videoArtifactID(roomID, id))
	switch {
	case errors.Is(err, errSiteNotFound):
		return wire.MediaVideoReply{}, false, nil
	case err != nil:
		return wire.MediaVideoReply{}, false, err
	}
	return wire.MediaVideoReply{ID: id, Status: wire.MediaVideoDone, ArtifactID: art.ArtifactID,
		FileName: firstNonEmpty(art.FileName, art.Title), Bytes: v.SizeBytes}, true, nil
}

// videoArtifactID is the room file a video is published as: derived from
// the room and the video's id, so every poll of a done video names the same
// file (and the same video in another room another one). The room service
// takes it as the caller's stable id (lower-case letters, digits and
// underscores).
func videoArtifactID(roomID, videoID string) string {
	sum := sha256.Sum256([]byte(roomID + "\n" + videoID))
	return "artv_" + hex.EncodeToString(sum[:20])
}

// roomFileCap is the room's blob version cap (the artifact list's
// maxBytes: the room service's own setting), read as the member in claims.
func (s *Service) roomFileCap(ctx context.Context, claims auth.Claims, roomID string) (int64, error) {
	var list wire.ArtifactList
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/artifacts?limit=1", "", nil, claims.Subject, claims.Tenant, &list); err != nil {
		return 0, err
	}
	if list.MaxBytes <= 0 {
		return 0, errors.New("the room's file list names no file cap")
	}
	return list.MaxBytes, nil
}

// maxVideoFileNameRunes bounds a published video's name (before .mp4).
const maxVideoFileNameRunes = 120

// videoFileName is a published video's name: the asked name's last path
// element without control characters or its extension, else "video",
// always ending .mp4 (the file is an MP4: media.SniffVideo).
func videoFileName(asked string) string {
	name := strings.TrimSpace(asked)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if !utf8.ValidString(name) {
		name = ""
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimSuffix(name, path.Ext(name)))
	if r := []rune(name); len(r) > maxVideoFileNameRunes {
		name = string(r[:maxVideoFileNameRunes])
	}
	if name == "" {
		name = "video"
	}
	return name + ".mp4"
}

// videoTestReply is a video provider test's answer: whether the provider's
// model list names the default model (some proxies list aliases only, so
// a model missing from it is said, not failed).
func videoTestReply(model string, models []string) string {
	if slices.Contains(models, model) {
		return model + " is among the provider's models"
	}
	return fmt.Sprintf("the provider answers, but its %d models do not name %s", len(models), model)
}
