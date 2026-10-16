package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/assist"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/media"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// app.ai (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md §6): an
// interactive app's page asks the assist model one question, POST ~app/ai
// {prompt, input, json}. It is an assist run in every way but its prompt:
// the caller's organization's model (or the platform's), off where the
// organization turned assists off, on the caller's per-member assist rate,
// audited as an assist with the app's id. The prompt is the app's, the
// input is fenced as data (assist.AppMessages). Only a signed-in caller
// may: an anonymous visitor and an agent are refused (an agent has its own
// model).

// appAIRequestBytes bounds an app.ai body: the prompt and the input, CJK
// text included.
const appAIRequestBytes = 4 * (wire.MaxAppAIPrompt + wire.MaxAppAIInput)

// errAppAIVisitor refuses app.ai to a caller who did not sign in.
var errAppAIVisitor = &appError{status: http.StatusForbidden, code: wire.AppSignInRequired, msg: "AI in this app is for people who signed in: open the link signed in"}

// serveAppAI answers POST ~app/ai.
func (s *Service) serveAppAI(w http.ResponseWriter, r *http.Request, c appCapability) {
	w.Header().Set("Cache-Control", "no-store")
	if c.scope.Kind != svcauth.AppMember {
		writeAppErr(w, errAppAIVisitor)
		return
	}
	claims := auth.Claims{Subject: c.scope.Participant, Tenant: c.tenant}
	if !s.assistLimiter.get(assistRate, assistBurst, s.Peers.Size).allow(claims.Tenant + "/" + claims.Subject) {
		httpx.Error(w, "too many AI requests: try again in a minute", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, appAIRequestBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	var req wire.AppAIRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	switch {
	case req.Prompt == "":
		httpx.Error(w, "ai needs a prompt", http.StatusBadRequest)
		return
	case !utf8.ValidString(req.Prompt) || utf8.RuneCountInString(req.Prompt) > wire.MaxAppAIPrompt:
		httpx.Error(w, fmt.Sprintf("the prompt is at most %d characters", wire.MaxAppAIPrompt), http.StatusBadRequest)
		return
	case !utf8.ValidString(req.Input) || utf8.RuneCountInString(req.Input) > wire.MaxAppAIInput:
		httpx.Error(w, fmt.Sprintf("the input is at most %d characters", wire.MaxAppAIInput), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	model, ok, err := s.assistModel(ctx, claims)
	if err != nil {
		obs.ServerError(w, "read the assist model failed", err)
		httpx.Error(w, "read the assist model failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.WriteJSON(w, http.StatusConflict, httpx.ErrorBody{Error: "AI is not available here: the organization has no AI model, or turned AI off", Code: wire.AppAIUnavailable})
		return
	}
	text, usage, err := s.assistClient().Complete(ctx, model, assist.AppMessages(req.Prompt, req.Input), req.JSON)
	var reply wire.AppAIReply
	if err == nil {
		if req.JSON {
			reply.JSON, err = assist.Object(text)
		} else {
			reply.Text = strings.TrimSpace(text)
		}
	}
	s.auditModelRun(ctx, claims, "app:"+c.scope.AppID, "room "+c.scope.RoomID, model, usage, err)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writeAssistModelErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, reply)
}

// serveAppAIImage answers POST ~app/ai/image (docs/prd/
// 2026-10-16-voice-and-image-generation.md §5): an app's page draws images
// on the organization's media provider — runMediaImage, the same rails as
// app.ai (a signed-in caller, the caller's assist rate, audited with the
// app's id, the images never audited).
func (s *Service) serveAppAIImage(w http.ResponseWriter, r *http.Request, c appCapability) {
	w.Header().Set("Cache-Control", "no-store")
	if c.scope.Kind != svcauth.AppMember {
		writeAppErr(w, errAppAIVisitor)
		return
	}
	s.runMediaImage(w, r, auth.Claims{Subject: c.scope.Participant, Tenant: c.tenant}, readJSONMediaImage, "app:"+c.scope.AppID+":image")
}

// serveAppAIVoice answers POST ~app/ai/voice: an app's page turns spoken
// audio into text on the organization's media provider. The same rails as
// app.ai: a signed-in caller, the caller's assist rate, audited as an
// assist run (never the audio, never the text).
func (s *Service) serveAppAIVoice(w http.ResponseWriter, r *http.Request, c appCapability) {
	w.Header().Set("Cache-Control", "no-store")
	if c.scope.Kind != svcauth.AppMember {
		writeAppErr(w, errAppAIVisitor)
		return
	}
	claims := auth.Claims{Subject: c.scope.Participant, Tenant: c.tenant}
	if !s.assistLimiter.get(assistRate, assistBurst, s.Peers.Size).allow(claims.Tenant + "/" + claims.Subject) {
		httpx.Error(w, "too many AI requests: try again in a minute", http.StatusTooManyRequests)
		return
	}
	audio, ok := readBody(w, r, wire.MaxMediaAudioBytes)
	if !ok {
		return
	}
	if len(audio) == 0 {
		httpx.Error(w, "the request body is the audio: it is empty", http.StatusBadRequest)
		return
	}
	mimeType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	ctx := r.Context()
	p, has, err := s.mediaProvider(ctx, claims, media.CapTranscribe)
	if err != nil {
		obs.ServerError(w, "read the media provider failed", err)
		httpx.Error(w, "read the media provider failed", http.StatusInternalServerError)
		return
	}
	if !has {
		httpx.Error(w, "voice input is not available here: the deployment has no transcription provider, or your organization turned AI off", http.StatusConflict)
		return
	}
	start := time.Now()
	text, err := p.Transcribe(ctx, audio, mimeType, strings.TrimSpace(r.URL.Query().Get("filename")))
	usage := wire.AssistUsage{Model: p.Model.Model, LatencyMs: time.Since(start).Milliseconds()}
	s.auditModelRun(ctx, claims, "app:"+c.scope.AppID+":voice", "room "+c.scope.RoomID, assist.Model(p.Model), usage, err)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writeMediaErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.MediaTranscribeReply{Text: text})
}
