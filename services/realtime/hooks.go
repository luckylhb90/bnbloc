package realtimehttp

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A task's webhook (docs/prd/2026-10-21-room-events.md §2.3): a task whose
// trigger subscribes to wire.SubscribeWebhook gets its own URL and a secret
// shown once, sealed at rest with the deployment's secret store key
// (sealbox, like component credentials). An outside system signs each
// request with it (schedule.VerifyHookSignature, GitHub's scheme).
//
//	POST   /v1/rooms/{roomId}/schedules/{scheduleId}/hook   creator: make it, or rotate its secret → wire.ScheduleHookSecret (shown once)
//	DELETE /v1/rooms/{roomId}/schedules/{scheduleId}/hook   creator, owner or admin: revoke it (the URL answers 404 after)
//	POST   /internal/v1/hooks/{hookId}                      the gateway's open route POST /v1/hooks/{hookId}, body as sent → wire.HookReply
//
// A request is bounded (schedule.MaxHookBodyBytes), verified, turned into
// the event it carries (schedule.HookEvent) and dispatched to the hook's own
// task only (dispatchEvent), within the task's rate limit. A request refused
// for its signature, size or shape is counted on the hook
// (store.CountHookRefused); one over the rate limit is a dropped event,
// counted on the task.

func (s *Service) registerHookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/rooms/{roomId}/schedules/{scheduleId}/hook", s.handlePutHook)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/schedules/{scheduleId}/hook", s.handleDeleteHook)
}

// handlePutHook makes the schedule's webhook, or rotates its secret (the
// URL stays): only its creator, whose task the requests run.
func (s *Service) handlePutHook(w http.ResponseWriter, r *http.Request) {
	roomID, userID, _, ok := s.room(w, r, membership.Chat)
	if !ok || !s.schedulesReady(w) {
		return
	}
	if s.HookBox == nil {
		httpx.Unavailable(w, "webhooks (the secret store key, "+sealbox.KeyEnv+")")
		return
	}
	cur, err := s.Schedules.GetSchedule(r.Context(), roomID, r.PathValue("scheduleId"))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	if cur.CreatedBy != userID {
		httpx.Error(w, "only the creator may make or rotate this task's webhook", http.StatusForbidden)
		return
	}
	if !schedule.OnEvent(cur.Recurrence) || cur.Recurrence.Source != wire.SubscribeWebhook {
		httpx.Error(w, "a webhook goes with a task whose trigger is "+wire.SubscribeWebhook, http.StatusConflict)
		return
	}
	hook := wire.ScheduleHook{HookID: schedule.NewHookID(), CreatedAtMs: time.Now().UnixMilli()}
	if cur.Hook != nil {
		hook.HookID = cur.Hook.HookID // a rotation keeps the URL
	}
	secret, err := schedule.NewHookSecret()
	if err != nil {
		obs.ServerError(w, "mint webhook secret failed", err)
		httpx.Error(w, "mint webhook secret failed", http.StatusInternalServerError)
		return
	}
	sealed, err := s.HookBox.Seal([]byte(secret), schedule.HookSecretAD(hook.HookID))
	if err != nil {
		obs.ServerError(w, "seal webhook secret failed", err)
		httpx.Error(w, "seal webhook secret failed", http.StatusInternalServerError)
		return
	}
	if err := s.Schedules.SetScheduleHook(r.Context(), roomID, cur.ScheduleID, hook, sealed); err != nil {
		writeScheduleErr(w, err)
		return
	}
	slog.Info("bnbloc realtime: webhook set", "event", "bnbloc.schedule.hook_set", "schedule", cur.ScheduleID, "room", roomID, "by", userID, "rotated", cur.Hook != nil)
	httpx.WriteJSON(w, http.StatusCreated, wire.ScheduleHookSecret{Hook: hook, Secret: secret})
}

// handleDeleteHook revokes the schedule's webhook: its creator, or the
// room's owner or an admin (a leaked URL is stopped by whoever sees it).
func (s *Service) handleDeleteHook(w http.ResponseWriter, r *http.Request) {
	roomID, userID, _, ok := s.room(w, r, membership.AnyMember)
	if !ok || !s.schedulesReady(w) {
		return
	}
	cur, err := s.Schedules.GetSchedule(r.Context(), roomID, r.PathValue("scheduleId"))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	moderator, err := s.canModerateSchedules(r.Context(), roomID, userID)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, "read membership failed", http.StatusInternalServerError)
		return
	}
	if cur.CreatedBy != userID && !moderator {
		httpx.Error(w, "only the creator, the room owner, or an admin may revoke this webhook", http.StatusForbidden)
		return
	}
	if err := s.Schedules.DeleteScheduleHook(r.Context(), roomID, cur.ScheduleID); err != nil {
		writeScheduleErr(w, err)
		return
	}
	slog.Info("bnbloc realtime: webhook revoked", "event", "bnbloc.schedule.hook_revoked", "schedule", cur.ScheduleID, "room", roomID, "by", userID)
	w.WriteHeader(http.StatusNoContent)
}

// handleInternalHook serves one request to a task's webhook.
func (s *Service) handleInternalHook(w http.ResponseWriter, r *http.Request) {
	if !s.schedulesReady(w) {
		return
	}
	hookID := r.PathValue("hookId")
	sc, sealed, err := s.Schedules.ScheduleByHook(r.Context(), hookID)
	if errors.Is(err, store.ErrScheduleNotFound) {
		httpx.Error(w, "no such webhook", http.StatusNotFound)
		return
	}
	if err != nil {
		obs.ServerError(w, "read webhook failed", err)
		httpx.Error(w, "read webhook failed", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	tctx := tenant.NewContext(r.Context(), sc.TenantID)
	refuse := func(status int, msg string) {
		if err := s.Schedules.CountHookRefused(tctx, hookID, now.UnixMilli()); err != nil {
			slog.Warn("bnbloc realtime: count refused webhook request failed", "hook", hookID, "error", err)
		}
		slog.Warn("bnbloc realtime: webhook request refused", "event", "bnbloc.schedule.hook_refused", "schedule", sc.ScheduleID, "room", sc.RoomID, "status", status, "reason", msg)
		httpx.WriteStatus(w, status, msg, nil)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, schedule.MaxHookBodyBytes+1))
	if err != nil {
		refuse(http.StatusBadRequest, "read body failed")
		return
	}
	if len(body) > schedule.MaxHookBodyBytes {
		refuse(http.StatusRequestEntityTooLarge, "the body is over the webhook's limit")
		return
	}
	if s.HookBox == nil {
		httpx.Unavailable(w, "webhooks (the secret store key, "+sealbox.KeyEnv+")")
		return
	}
	secret, err := s.HookBox.Open(sealed, schedule.HookSecretAD(hookID))
	if err != nil {
		obs.ServerError(w, "open webhook secret failed", err)
		httpx.Error(w, "open webhook secret failed", http.StatusInternalServerError)
		return
	}
	if !schedule.VerifyHookSignature(r.Header, string(secret), body) {
		refuse(http.StatusUnauthorized, "the request is not signed with this webhook's secret ("+wire.HookSignatureHeader+" or "+wire.GitHubSignatureHeader+": sha256=<hex HMAC-SHA256 of the body>)")
		return
	}
	ev, err := schedule.HookEvent(hookID, r.Header, body, now)
	if err != nil {
		refuse(http.StatusBadRequest, err.Error())
		return
	}
	n, dropped, err := s.dispatchEvent(tctx, eventDelivery{event: ev, only: &sc}, now)
	if err != nil {
		obs.ServerError(w, "dispatch webhook event failed", err)
		httpx.Error(w, "dispatch webhook event failed", http.StatusInternalServerError)
		return
	}
	if dropped > 0 {
		httpx.Error(w, "this webhook's task is taking events faster than its limit: the event was dropped", http.StatusTooManyRequests)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, wire.HookReply{Triggered: n})
}
