package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// app.notify (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md §5):
// POST ~app/notify {to, title, text, url} sends a notice of type
// app_notice through the notification emitter, the one producer seam: the
// recipients' preferences (the "App notices" switch) and muted rooms
// apply, and the organization's notifiers, a member's desktop plugin or
// browser push deliver it. The room service resolves who to reaches
// (POST /internal/v1/app/notify-targets); the gateway bounds how often
// (per app and per caller), signs the notice with the app's title and the
// room, links it to the app, and audits it (kind app, never its text).

// App notice rates: per app (every caller together) and per caller, on top
// of the app data write budgets the call spends. A reminder app sends a
// few a week; a loop is stopped within its burst.
const (
	appNotifyAppRate     = 30.0 / 3600.0
	appNotifyAppBurst    = 30.0
	appNotifyCallerRate  = 10.0 / 3600.0
	appNotifyCallerBurst = 10.0
)

// appNoticeDedupWindow collapses the same notice (app, to, title, text)
// sent again within it: a double click, a page reloaded mid-send.
const appNoticeDedupWindow = 10 * time.Minute

// serveAppNotify answers POST ~app/notify.
func (s *Service) serveAppNotify(w http.ResponseWriter, r *http.Request, c appCapability) {
	w.Header().Set("Cache-Control", "no-store")
	if c.scope.Kind != svcauth.AppMember {
		writeAppErr(w, &appError{status: http.StatusForbidden, code: wire.AppSignInRequired, msg: "notices are for people who signed in: open the link signed in"})
		return
	}
	if s.Alerts == nil {
		httpx.Unavailable(w, "notifications")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, wire.MaxAppRequestBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	var in wire.AppNotifyRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	in.Title, in.Text, in.URL = strings.TrimSpace(in.Title), strings.TrimSpace(in.Text), strings.TrimSpace(in.URL)
	switch {
	case in.Title == "":
		httpx.Error(w, "notify needs a title", http.StatusBadRequest)
		return
	case !utf8.ValidString(in.Title) || utf8.RuneCountInString(in.Title) > notify.MaxAlertTitleRunes:
		httpx.Error(w, fmt.Sprintf("the title is at most %d characters", notify.MaxAlertTitleRunes), http.StatusBadRequest)
		return
	case !utf8.ValidString(in.Text) || utf8.RuneCountInString(in.Text) > notify.MaxAlertTextRunes:
		httpx.Error(w, fmt.Sprintf("the text is at most %d characters", notify.MaxAlertTextRunes), http.StatusBadRequest)
		return
	}
	link, ok := s.appNoticeURL(r, c, in.URL)
	if !ok {
		httpx.Error(w, "url must be a link on this deployment", http.StatusBadRequest)
		return
	}
	caller := c.tenant + "\x00" + c.scope.Participant
	if !s.appNotifyLimiters.app.Get(appNotifyAppRate, appNotifyAppBurst, s.replicaCount).Allow(c.tenant+"\x00"+c.scope.AppID) ||
		!s.appNotifyLimiters.caller.Get(appNotifyCallerRate, appNotifyCallerBurst, s.replicaCount).Allow(caller) {
		httpx.Error(w, "too many notices from this app: try again later", http.StatusTooManyRequests)
		return
	}
	targets, status, body, err := s.appNotifyTargets(r.Context(), c, raw)
	if err != nil {
		obs.ServerError(w, "resolve the notice's recipients failed", err)
		httpx.Error(w, "resolve the notice's recipients failed", http.StatusBadGateway)
		return
	}
	if status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	title := in.Title
	if t := strings.TrimSpace(targets.AppTitle); t != "" {
		title = t + ": " + title
	}
	dedup := fmt.Sprintf("app:%s:%s:%x", c.scope.AppID, s.nowTime().Truncate(appNoticeDedupWindow).Format(time.RFC3339), noticeDigest(string(raw)))
	ev := notify.Event{
		Type: wire.NotificationAppNotice, RoomID: c.scope.RoomID, RoomName: targets.RoomName, Recipients: targets.Recipients,
		Actor: c.scope.Participant, Title: title, Text: in.Text, URL: link, DedupKey: dedup,
	}
	ctx := tenant.NewContext(context.WithoutCancel(r.Context()), c.tenant)
	if _, err := s.sendNotice(ctx, ev, targets); err != nil {
		if errors.Is(err, notify.ErrNoGroupNotifier) {
			httpx.WriteJSON(w, http.StatusConflict, httpx.ErrorBody{Error: "the group chat's notifier no longer delivers here: connect the group again", Code: wire.AppGroupsUnavailable})
			return
		}
		obs.ServerError(w, "queue the notice failed", err)
		httpx.Error(w, "queue the notice failed", http.StatusInternalServerError)
		return
	}
	s.audit(ctx, c.tenant, store.ExtAuditRecord{
		Kind: wire.AuditKindApp, Name: c.scope.AppID, Action: "notify", Actor: c.scope.Participant,
		Outcome: wire.AuditOutcomeAllow, Detail: noticeAuditDetail(c.scope.RoomID, targets),
	})
	httpx.WriteJSON(w, http.StatusOK, wire.AppNotifyReply{Recipients: len(targets.Recipients)})
}

// sendNotice queues one notice: to its connected group chat through the
// notifier it was connected with (notify.ErrNoGroupNotifier when that
// notifier no longer delivers to the tenant or to groups), else to its
// members.
func (s *Service) sendNotice(ctx context.Context, ev notify.Event, targets wire.AppNotifyTargets) (queued bool, err error) {
	if targets.Group != "" {
		ev.Group, ev.GroupNotifier, ev.GroupAddress = targets.Group, targets.GroupNotifier, targets.GroupAddress
		return s.Alerts.EmitGroup(ctx, ev)
	}
	return s.Alerts.Emit(ctx, ev)
}

// noticeAuditDetail is a notice's audit detail: where it went, never what
// it said.
func noticeAuditDetail(roomID string, t wire.AppNotifyTargets) string {
	if t.Group != "" {
		return fmt.Sprintf("room %s, group %s", roomID, t.Group)
	}
	return fmt.Sprintf("room %s, %d recipients", roomID, len(t.Recipients))
}

// appNotifyTargets asks the room service who the notice reaches, as the
// call's app scope; a refusal comes back as its status and body.
func (s *Service) appNotifyTargets(ctx context.Context, c appCapability, body []byte) (wire.AppNotifyTargets, int, []byte, error) {
	var out wire.AppNotifyTargets
	status, raw, err := s.appBackend(ctx, c, http.MethodPost, "/notify-targets", body)
	if err != nil || status != http.StatusOK {
		return out, status, raw, err
	}
	return out, status, nil, json.Unmarshal(raw, &out)
}

// appBackend makes one call of the room service's app data API
// (/internal/v1/app{path}) in c's scope, signed as forwardApp signs it, and
// returns its status and body: the gateway's own calls (a notice's
// recipients, a schedule run's reads), where forwardApp relays a client's.
func (s *Service) appBackend(ctx context.Context, c appCapability, method, path string, body []byte) (int, []byte, error) {
	base := strings.TrimRight(s.Backends.Room, "/")
	if base == "" {
		return 0, nil, errors.New("no room service")
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	key := svcauth.Key(svcauth.KeyFromEnv())
	req, err := key.NewRequest(ctx, method, base+"/internal/v1/app"+path, rd, gatewayServiceSubject, c.tenant)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := key.AssertApp(req, c.scope); err != nil {
		return 0, nil, err
	}
	resp, err := backendHTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, raw, err
}

// appNoticeURL is a notice's link: the app's card in its room by default;
// a url the app passes only when it is on this deployment's own origin (a
// notice never carries a link to elsewhere).
func (s *Service) appNoticeURL(r *http.Request, c appCapability, raw string) (string, bool) {
	origin := s.publicOrigin(r)
	if raw == "" {
		return origin + "/#/room/" + url.PathEscape(c.scope.RoomID) + "?artifact=" + url.QueryEscape(c.scope.AppID), true
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || len(raw) > 2048 {
		return "", false
	}
	o, err := url.Parse(origin)
	if err != nil || u.Scheme != o.Scheme || u.Host != o.Host {
		return "", false
	}
	return u.String(), true
}

// noticeDigest identifies a notice's content (to, title, text, url).
func noticeDigest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:8]
}
