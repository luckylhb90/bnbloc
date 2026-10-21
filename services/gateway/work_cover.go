package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/media"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// AI covers (docs/prd/2026-10-19-work-covers.md §2.2): the image provider
// draws a work's cover from its title, description and type, as the member
// asking (an image call's rate limit, budget and audit). The prompt is
// here and nowhere else.

// coverPromptFor is the cover prompt for a work. The title and description
// are the member's own words; the picture carries no text, which an image
// model renders badly and a card already shows.
func coverPromptFor(title, description, workType string) string {
	what := map[string]string{
		wire.WorkTypeApp: "an interactive web app", wire.WorkTypeSite: "a web page", wire.WorkTypeImage: "an image",
		wire.WorkTypeVideo: "a video", wire.WorkTypeAudio: "an audio recording", wire.WorkTypeDocument: "a document",
		wire.WorkTypeSlides: "a slide deck", wire.WorkTypeTable: "a table of data",
	}[workType]
	if what == "" {
		what = "a piece of work"
	}
	var b strings.Builder
	b.WriteString("A cover picture for " + what + " titled " + strconv.Quote(strings.TrimSpace(title)))
	if d := strings.TrimSpace(description); d != "" {
		b.WriteString(": " + d)
	}
	b.WriteString(". One clear, striking illustration of what it is about, for a gallery card. No text, letters, words, logos or user interface.")
	return b.String()
}

// errCoverUnavailable: the organization has no image provider or AI off.
var errCoverUnavailable = errors.New("AI covers need the organization's image generation")

// drawWorkCover draws a cover as claims (ownAuth: the caller's own session
// for a caller-auth provider) and fits it to the cover cap. It answers the
// cover and the model; errCoverUnavailable when no provider serves the
// member.
func (s *Service) drawWorkCover(ctx context.Context, claims auth.Claims, ownAuth, title, description, workType string) ([]byte, string, error) {
	p, has, err := s.mediaProvider(ctx, claims, media.CapImage, ownAuth)
	if err != nil {
		return nil, "", err
	}
	if !has {
		return nil, "", errCoverUnavailable
	}
	start := time.Now()
	imgs, err := p.Generate(ctx, media.ImageRequest{Prompt: coverPromptFor(title, description, workType), Aspect: wire.MediaImageAspectLandscape, Count: 1})
	s.auditMediaRun(ctx, claims, "work-cover", "work cover", wire.AssistUsage{Model: p.Model.Model, LatencyMs: timeSinceMs(s, start)}, err)
	if err != nil {
		return nil, "", err
	}
	if len(imgs) == 0 {
		return nil, "", errors.New("the provider drew nothing")
	}
	cover, err := tarbundle.FitCover(imgs[0].Bytes)
	if err != nil {
		return nil, "", err
	}
	return cover, p.Model.Model, nil
}

// handleDrawWorkCover answers POST
// /v1/rooms/{roomId}/artifacts/{artifactId}/work-cover: an AI cover for the
// deliverable the member is about to publish (who may publish it may ask),
// as a data URL the publish request carries back when they keep it.
func (s *Service) handleDrawWorkCover(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	ctx := r.Context()
	roomID, artifactID := r.PathValue("roomId"), r.PathValue("artifactId")
	actor, ok := s.deliverableOwner(w, r, caller, roomID)
	if !ok {
		return
	}
	if !s.assistLimiter.Get(assistRate, assistBurst, s.Peers.Size).Allow(actor.Claims.Tenant + "/" + actor.Claims.Subject) {
		httpx.Error(w, "too many requests: try again in a minute", http.StatusTooManyRequests)
		return
	}
	raw, ok := readBody(w, r, maxWorkRequestBytes)
	if !ok {
		return
	}
	var in wire.WorkCoverRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			httpx.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
	}
	art, _, err := s.deliverableVersion(ctx, actor.Claims, roomID, artifactID)
	if !writeDeliverableErr(w, err) {
		return
	}
	workType := contentpkg.WorkTypeOf(contentpkg.WorkDeliverable{Artifact: art, FileName: firstNonEmpty(art.FileName, art.Title)})
	cover, model, err := s.drawWorkCover(ctx, actor.Claims, ownAuthOf(r, actor), firstNonEmpty(in.DisplayName, workTitle(art)), in.Description, workType)
	if !writeCoverErr(w, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.WorkCoverReply{Cover: coverDataURL(cover), Model: model})
}

// writeCoverErr answers a failed AI cover (false), or reports none (true).
func writeCoverErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, errCoverUnavailable):
		httpx.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, tarbundle.ErrCoverNotImage), errors.Is(err, tarbundle.ErrCoverTooLarge):
		obs.ServerError(w, "fit the drawn cover failed", err)
		httpx.Error(w, "the drawn picture could not be made a cover: try again", http.StatusBadGateway)
	default:
		writeMediaErr(w, err)
	}
	return false
}

// coverDataURL is a cover image as a data URL (its type sniffed).
func coverDataURL(cover []byte) string {
	return "data:" + http.DetectContentType(cover) + ";base64," + base64.StdEncoding.EncodeToString(cover)
}
