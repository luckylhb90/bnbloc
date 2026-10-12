package notify

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Request bounds for the settings endpoints.
const (
	maxQuietRooms = 500
	maxBodyBytes  = 64 * 1024
)

// Handlers serves the member notification API:
//
//	GET/PUT /v1/my/notification-prefs    (own preferences)
//	GET     /v1/my/notifiers             (notifier components visible to the member, opt-in state)
//	POST    /v1/my/notifiers/{name}/test (test notification to the caller only)
//
// Notifiers hold no per-member settings: a member's own delivery (a personal
// bot) is a plugin on their desktop (notification.v1 hook, desktop-local
// settings), never a server-side secret.
//
// The caller is the svcauth-verified X-Bnbloc-User and the tenant is the
// signed context tenant; nothing is taken from the body.
type Handlers struct {
	Prefs     store.NotificationPrefsStore
	Notifiers *Directory
	Installs  store.ComponentInstallStore
	Secrets   *SecretStore
	PublicURL string
	Audit     store.ExtensionAuditStore
	Now       func() time.Time
}

// Register mounts the routes on mux.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/my/notification-prefs", h.handleGetPrefs)
	mux.HandleFunc("PUT /v1/my/notification-prefs", h.handlePutPrefs)
	mux.HandleFunc("GET /v1/my/notifiers", h.handleListNotifiers)
	mux.HandleFunc("POST /v1/my/notifiers/{name}/test", h.handleTestNotifier)
}

func (h *Handlers) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func caller(w http.ResponseWriter, r *http.Request) (string, bool) {
	u := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if u == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return "", false
	}
	return u, true
}

func (h *Handlers) handleGetPrefs(w http.ResponseWriter, r *http.Request) {
	user, ok := caller(w, r)
	if !ok {
		return
	}
	p, found, err := h.Prefs.GetNotificationPrefs(r.Context(), user)
	if err != nil {
		obs.ServerError(w, "read notification prefs failed", err)
		writeErr(w, http.StatusInternalServerError, "read notification prefs failed")
		return
	}
	if !found {
		p = store.DefaultNotificationPrefs()
	}
	writeJSON(w, http.StatusOK, prefsDTO(p))
}

func (h *Handlers) handlePutPrefs(w http.ResponseWriter, r *http.Request) {
	user, ok := caller(w, r)
	if !ok {
		return
	}
	var body wire.NotificationPrefs
	if !decodeBody(w, r, &body) {
		return
	}
	quiet, err := normalizeQuietRooms(body.QuietRooms)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p := store.NotificationPrefs{
		Mentions: body.Mentions, AgentFinished: body.AgentFinished, AgentFailed: body.AgentFailed,
		BorrowRequests: body.BorrowRequests, ContentShared: body.ContentShared, ContentReview: body.ContentReview,
		QuietRooms: quiet, UpdatedAtMs: h.now().UnixMilli(),
	}
	if err := h.Prefs.PutNotificationPrefs(r.Context(), user, p); err != nil {
		obs.ServerError(w, "save notification prefs failed", err)
		writeErr(w, http.StatusInternalServerError, "save notification prefs failed")
		return
	}
	writeJSON(w, http.StatusOK, prefsDTO(p))
}

func normalizeQuietRooms(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if err := roomctx.ValidateID(id); err != nil {
			return nil, errors.New("quietRooms: invalid room id " + id)
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) > maxQuietRooms {
		return nil, errors.New("quietRooms: too many rooms")
	}
	return out, nil
}

func prefsDTO(p store.NotificationPrefs) wire.NotificationPrefs {
	quiet := p.QuietRooms
	if quiet == nil {
		quiet = []string{}
	}
	return wire.NotificationPrefs{
		Mentions: p.Mentions, AgentFinished: p.AgentFinished, AgentFailed: p.AgentFailed,
		BorrowRequests: p.BorrowRequests, ContentShared: p.ContentShared, ContentReview: p.ContentReview, QuietRooms: quiet,
	}
}

// visibleNotifier resolves {name} to a notifier record distributed to the
// caller's tenant, writing 404 otherwise.
func (h *Handlers) visibleNotifier(w http.ResponseWriter, r *http.Request) (extpipe.Record, bool) {
	rec, found, err := h.Notifiers.Record(r.Context(), r.PathValue("name"))
	if err != nil {
		obs.ServerError(w, "read notifiers failed", err)
		writeErr(w, http.StatusInternalServerError, "read notifiers failed")
		return extpipe.Record{}, false
	}
	if !found || !Distributes(rec, tenant.FromContext(r.Context())) {
		writeErr(w, http.StatusNotFound, "notifier not found")
		return extpipe.Record{}, false
	}
	return rec, true
}

func (h *Handlers) handleListNotifiers(w http.ResponseWriter, r *http.Request) {
	user, ok := caller(w, r)
	if !ok {
		return
	}
	recs, err := h.Notifiers.Records(r.Context())
	if err != nil {
		obs.ServerError(w, "read notifiers failed", err)
		writeErr(w, http.StatusInternalServerError, "read notifiers failed")
		return
	}
	tid := tenant.FromContext(r.Context())
	out := []wire.MemberNotifier{}
	for _, rec := range recs {
		if !Distributes(rec, tid) {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil {
			continue
		}
		optIn := extpipe.NormalizeRollout(rec.Rollout) == extpipe.RolloutOptIn
		n := wire.MemberNotifier{
			Name: rec.Name, Description: cfg.Description, OptIn: optIn,
			Installed: !optIn || MemberInstalled(r.Context(), h.Installs, user, rec.Name),
		}
		out = append(out, n)
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifiers": out})
}

// handleTestNotifier sends one signed "test" notification to the caller only,
// through the notifier's (organization) delivery, and reports the notifier's
// answer, so a member sees at once whether it reaches them. It bypasses the
// outbox: a test is never retried.
func (h *Handlers) handleTestNotifier(w http.ResponseWriter, r *http.Request) {
	user, ok := caller(w, r)
	if !ok {
		return
	}
	rec, ok := h.visibleNotifier(w, r)
	if !ok {
		return
	}
	ep, live := h.Notifiers.Live(rec)
	if !live {
		obs.ServerError(w, "notifier is not active yet; try again shortly", nil)
		writeErr(w, http.StatusServiceUnavailable, "notifier is not active yet; try again shortly")
		return
	}
	secret, found, err := h.Secrets.Get(r.Context(), rec.Name)
	if err != nil || !found {
		obs.ServerError(w, "notifier signing secret unavailable", err)
		writeErr(w, http.StatusServiceUnavailable, "notifier signing secret unavailable")
		return
	}
	tid := tenant.FromContext(r.Context())
	now := h.now()
	p := TestPayload(tid, user, h.PublicURL, now)
	res := SendTest(r.Context(), ep, secret, p, now)
	if h.Audit != nil {
		outcome := "delivered"
		if !res.Delivered {
			outcome = "failed"
		}
		_ = h.Audit.AppendExtAudit(r.Context(), store.ExtAuditRecord{
			Kind: string(extreg.KindNotifier), Name: rec.Name, Action: "test", Actor: user, TenantID: tid,
			Outcome: outcome, Detail: strings.TrimSpace(res.EventID + " " + res.Error), AtMs: now.UnixMilli(),
		})
	}
	writeJSON(w, http.StatusOK, res)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
