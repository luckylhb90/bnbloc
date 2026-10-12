package shareagenthttp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Audit log (R13): one read API over every audit trail, merged server-side in
// one total order (store.MergeAuditLog) with an opaque cursor.
//
//   - GET /v1/tenant/audit-log   — the ctx tenant only. The gateway gates
//     /v1/tenant/* on manage_org (org_admin, super_admin acting in a tenant),
//     and the tenant is the signed ctx tenant, so an org admin can never name
//     another tenant: no tenant parameter is read here.
//   - GET /v1/platform/audit-log — every tenant (plus platform-level rows with
//     no tenant), or one tenant with ?tenant=. The gateway gates /v1/platform/*
//     on manage_platform (super_admin only).
//
// Query: kind (comma list of kinds), actor, outcome (exact), from/to (unix ms
// or YYYY-MM-DD in tzOffsetMin's zone; to exclusive), cursor, limit (default
// 100, max 500), format=csv (the whole filtered range, newest first, capped at
// maxAuditExportRows).

const (
	defaultAuditPage   = 100
	maxAuditPage       = 500
	maxAuditExportRows = 20_000
)

func (s *Service) handleTenantAuditLog(w http.ResponseWriter, r *http.Request) {
	tid := tenant.FromContext(r.Context())
	if tid == "" {
		httpx.Error(w, "missing tenant scope", http.StatusBadRequest)
		return
	}
	s.serveAuditLog(w, r, store.AuditLogQuery{TenantID: tid})
}

func (s *Service) handlePlatformAuditLog(w http.ResponseWriter, r *http.Request) {
	q := store.AuditLogQuery{AllTenants: true}
	if t := strings.TrimSpace(r.URL.Query().Get("tenant")); t != "" {
		q = store.AuditLogQuery{TenantID: t}
	}
	s.serveAuditLog(w, r, q)
}

func (s *Service) serveAuditLog(w http.ResponseWriter, r *http.Request, scope store.AuditLogQuery) {
	if len(s.AuditLog) == 0 {
		httpx.Unavailable(w, "audit log")
		return
	}
	q, csvOut, msg := parseAuditLogQuery(r, scope)
	if msg != "" {
		httpx.Error(w, msg, http.StatusBadRequest)
		return
	}
	if csvOut {
		s.writeAuditCSV(w, r, q)
		return
	}
	rows, next, err := store.MergeAuditLog(r.Context(), q, s.AuditLog...)
	if err != nil {
		obs.ServerError(w, "audit log read failed", err)
		httpx.Error(w, "audit log read failed", http.StatusInternalServerError)
		return
	}
	reply := wire.AuditLogReply{Entries: auditEntries(rows)}
	if next != nil {
		reply.NextCursor = encodeAuditCursor(*next)
	}
	httpx.WriteJSON(w, http.StatusOK, reply)
}

// parseAuditLogQuery reads the filters onto the caller-decided scope.
func parseAuditLogQuery(r *http.Request, scope store.AuditLogQuery) (store.AuditLogQuery, bool, string) {
	v := r.URL.Query()
	q := scope
	q.Limit = defaultAuditPage
	if n := v.Get("limit"); n != "" {
		lim, err := strconv.Atoi(n)
		if err != nil || lim <= 0 {
			return q, false, "limit must be a positive integer"
		}
		q.Limit = min(lim, maxAuditPage)
	}
	for _, k := range strings.Split(v.Get("kind"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			q.Kinds = append(q.Kinds, k)
		}
	}
	q.Actor = strings.TrimSpace(v.Get("actor"))
	q.Outcome = strings.TrimSpace(v.Get("outcome"))
	loc := time.UTC
	if tz := v.Get("tzOffsetMin"); tz != "" {
		n, err := strconv.Atoi(tz)
		if err != nil || n < -14*60 || n > 14*60 {
			return q, false, "tzOffsetMin must be within ±840"
		}
		loc = time.FixedZone("client", -n*60)
	}
	if f := v.Get("from"); f != "" {
		t, ok := parseReportTime(f, loc)
		if !ok {
			return q, false, "from must be unix ms or YYYY-MM-DD"
		}
		q.FromMs = t.UnixMilli()
	}
	if f := v.Get("to"); f != "" {
		t, ok := parseReportTime(f, loc)
		if !ok {
			return q, false, "to must be unix ms or YYYY-MM-DD"
		}
		q.ToMs = t.UnixMilli()
	}
	if q.FromMs > 0 && q.ToMs > 0 && q.ToMs <= q.FromMs {
		return q, false, "to must be after from"
	}
	if c := v.Get("cursor"); c != "" {
		cur, ok := decodeAuditCursor(c)
		if !ok {
			return q, false, "invalid cursor"
		}
		q.After = &cur
	}
	format := v.Get("format")
	if format != "" && format != "csv" && format != "json" {
		return q, false, "format must be json or csv"
	}
	return q, format == "csv", ""
}

// encodeAuditCursor renders a merge position as an opaque token.
func encodeAuditCursor(c store.AuditCursor) string {
	raw := strconv.FormatInt(c.AtMs, 10) + "." + c.Source + "." + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeAuditCursor(token string) (store.AuditCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return store.AuditCursor{}, false
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 || (parts[1] != store.AuditSourceExtension && parts[1] != store.AuditSourceBorrow) {
		return store.AuditCursor{}, false
	}
	at, err1 := strconv.ParseInt(parts[0], 10, 64)
	id, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil {
		return store.AuditCursor{}, false
	}
	return store.AuditCursor{AtMs: at, Source: parts[1], ID: id}, true
}

func auditEntries(rows []store.AuditLogRow) []wire.AuditLogEntry {
	out := make([]wire.AuditLogEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, wire.AuditLogEntry{
			ID: r.Source + ":" + strconv.FormatInt(r.ID, 10), Source: r.Source, Kind: r.Kind, Name: r.Name,
			Action: r.Action, Actor: r.Actor, TenantID: r.TenantID, Outcome: r.Outcome, Detail: r.Detail,
			RoomID: r.RoomID, AtMs: r.AtMs,
		})
	}
	return out
}

// writeAuditCSV pages the whole filtered range into one download. A range
// beyond maxAuditExportRows is cut at the cap and flagged in a header so the
// caller can narrow the range.
func (s *Service) writeAuditCSV(w http.ResponseWriter, r *http.Request, q store.AuditLogQuery) {
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"time_utc", "at_ms", "tenant_id", "kind", "action", "name", "actor", "outcome", "room_id", "detail", "source", "id"})
	q.Limit = maxAuditPage
	written, truncated := 0, false
	for {
		rows, next, err := store.MergeAuditLog(r.Context(), q, s.AuditLog...)
		if err != nil {
			obs.ServerError(w, "audit log export failed", err)
			httpx.Error(w, "audit log export failed", http.StatusInternalServerError)
			return
		}
		for _, e := range auditEntries(rows) {
			if written == maxAuditExportRows {
				truncated = true
				break
			}
			_ = cw.Write([]string{
				time.UnixMilli(e.AtMs).UTC().Format(time.RFC3339), strconv.FormatInt(e.AtMs, 10), csvCell(e.TenantID),
				csvCell(e.Kind), csvCell(e.Action), csvCell(e.Name), csvCell(e.Actor), csvCell(e.Outcome),
				csvCell(e.RoomID), csvCell(e.Detail), e.Source, e.ID,
			})
			written++
		}
		if truncated || next == nil {
			break
		}
		q.After = next
	}
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit-log.csv"`)
	if truncated {
		w.Header().Set("X-Bnbloc-Truncated", strconv.Itoa(maxAuditExportRows))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// audit appends one administration event to the shared audit trail
// (extension_audit). Best-effort: the action already happened, so a failed
// write is logged, never surfaced to the caller.
func (s *Service) audit(ctx context.Context, rec store.ExtAuditRecord) {
	if s.ExtAudit == nil {
		return
	}
	if rec.AtMs == 0 {
		rec.AtMs = s.nowMs()
	}
	if rec.Outcome == "" {
		rec.Outcome = "ok"
	}
	if err := s.ExtAudit.AppendExtAudit(ctx, rec); err != nil {
		slog.Warn("bnbloc shareagent: audit append failed", "event", "bnbloc.shareagent.audit_append_failed",
			"kind", rec.Kind, "action", rec.Action, "error", err)
	}
}

// auditMember records an org membership change in the ctx tenant.
func (s *Service) auditMember(r *http.Request, userID, action, detail string) {
	s.audit(r.Context(), store.ExtAuditRecord{
		Kind: wire.AuditKindMember, Name: userID, Action: action, Actor: callerSubject(r),
		TenantID: tenant.FromContext(r.Context()), Detail: detail,
	})
}

// auditTenant records a platform tenant change; the row belongs to the
// changed tenant so its org admins see it too.
func (s *Service) auditTenant(r *http.Request, tenantID, action, detail string) {
	s.audit(r.Context(), store.ExtAuditRecord{
		Kind: wire.AuditKindTenant, Name: tenantID, Action: action, Actor: callerSubject(r),
		TenantID: tenantID, Detail: detail,
	})
}
