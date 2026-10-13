package room

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/audit"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Reading an app's data out (docs/prd/2026-10-12-interactive-apps.md §4.7):
// an owner's export, streamed, and the audit of every export and every
// agent read. auditAppRead is the one writer of those rows and
// appReadAudited the one rule of which reads get one.

// The audited reads of app data (the audit row's action).
const (
	appReadFind   = "find"
	appReadGet    = "get"
	appReadExport = "export"
)

// appReadAudited is which reads of app data are audited: every export, and
// every read of documents by an agent (find, get) through its job's grant.
// A member's or a visitor's reads through the app itself are not: they are
// the app being used.
func appReadAudited(a *appCall, action string) bool {
	return action == appReadExport || a.kind == svcauth.AppAgent
}

// auditAppRead records one read call of a's app (not one row per
// document) when appReadAudited says so: who (a member's subject; an
// agent's owner, the agent named in the detail), the app, the action, the
// collection ("" = every collection), the format of an export and the
// number of documents read. err is a read that failed part way.
func (s *Service) auditAppRead(a *appCall, action, collection, format string, rows int, err error) {
	if !appReadAudited(a, action) {
		return
	}
	actor := a.subject
	var detail []string
	if a.kind == svcauth.AppAgent {
		agent := strings.TrimPrefix(a.participant, string(svcauth.AppAgent)+":")
		actor, _, _ = strings.Cut(agent, "/")
		detail = append(detail, "agent="+agent)
	}
	if collection == "" {
		collection = "*"
	}
	detail = append(detail, "room="+a.scope.RoomID, "collection="+collection, "rows="+strconv.Itoa(rows))
	if format != "" {
		detail = append(detail, "format="+format)
	}
	outcome := wire.AuditOutcomeOK
	if err != nil {
		outcome = wire.AuditOutcomeFailed
		detail = append(detail, "error="+err.Error())
	}
	audit.Record(context.WithoutCancel(a.ctx), s.Audit, store.ExtAuditRecord{
		Kind: wire.AuditKindApp, Name: a.scope.AppID, Action: action, Actor: actor, TenantID: a.tenantID,
		Outcome: outcome, Detail: strings.Join(detail, " "), AtMs: s.Now(),
	})
}

// eachAppDoc calls fn with every document of collection, rendered for the
// owner, in _id order, a page at a time.
func (s *Service) eachAppDoc(a *appCall, collection string, fn func(doc map[string]any) error) error {
	if a.key == nil {
		return nil
	}
	q := store.AppDocQuery{AppScope: a.scope, Collection: collection, AllDocs: true, Limit: wire.MaxAppFindLimit}
	for {
		docs, err := s.AppDocs.FindAppDocs(a.ctx, q)
		if err != nil {
			return err
		}
		for _, d := range docs {
			doc, err := a.render(d)
			if err != nil {
				return err
			}
			if err := fn(doc); err != nil {
				return err
			}
			q.After = d.DocID
		}
		if len(docs) < q.Limit {
			return nil
		}
	}
}

// exportWriter tracks whether an export's answer has begun: a failure
// before it answers a status, after it aborts the response (the client
// sees a broken download, never a short file that looks whole).
type exportWriter struct {
	w       http.ResponseWriter
	started bool
}

func (e *exportWriter) Write(p []byte) (int, error) {
	e.started = true
	return e.w.Write(p)
}

// handleAppExport is GET …/export?collection=&format=json|csv (owners): the
// app's data streamed, every collection or the one named as JSON
// ({"collections": {name: [document, …]}}), one collection as CSV
// (appdata's CSV form: its columns are collected in a first pass, so the
// header is complete before any row). One audit row records it.
func (s *Service) handleAppExport(w http.ResponseWriter, r *http.Request, a *appCall) {
	query := r.URL.Query()
	format, collection := query.Get("format"), query.Get("collection")
	if format == "" {
		format = wire.AppExportJSON
	}
	switch {
	case format != wire.AppExportJSON && format != wire.AppExportCSV:
		writeAppError(w, appdataInvalid("format is json or csv"))
		return
	case collection != "" && !appdata.ValidCollection(collection):
		writeAppError(w, appdataInvalid("collection name"))
		return
	case format == wire.AppExportCSV && collection == "":
		writeAppError(w, appdataInvalid("a CSV export names its collection"))
		return
	}
	out := &exportWriter{w: w}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	var (
		rows int
		err  error
	)
	if format == wire.AppExportCSV {
		h.Set("Content-Type", "text/csv; charset=utf-8")
		rows, err = s.exportCSV(out, a, collection)
	} else {
		h.Set("Content-Type", "application/json; charset=utf-8")
		rows, err = s.exportJSON(out, a, collection)
	}
	s.auditAppRead(a, appReadExport, collection, format, rows, err)
	switch {
	case err == nil:
	case !out.started:
		h.Del("Content-Type")
		writeAppError(w, err)
	default:
		slog.Error("app data export failed after it began", "event", "bnbloc.app.export_failed", "app", a.scope.AppID, "error", err)
		panic(http.ErrAbortHandler)
	}
}

// exportJSON writes the JSON export of collection ("" = every collection,
// by name) and answers how many documents it wrote.
func (s *Service) exportJSON(w io.Writer, a *appCall, collection string) (int, error) {
	names := []string{collection}
	if collection == "" {
		counts, err := s.AppDocs.CountAppDocs(a.ctx, a.scope)
		if err != nil {
			return 0, err
		}
		names = names[:0]
		for name := range counts.Collections {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	rows := 0
	if _, err := io.WriteString(w, `{"collections":{`); err != nil {
		return rows, err
	}
	for i, name := range names {
		key, _ := json.Marshal(name)
		sep := ""
		if i > 0 {
			sep = ","
		}
		if _, err := io.WriteString(w, sep+"\n"+string(key)+":["); err != nil {
			return rows, err
		}
		first := true
		err := s.eachAppDoc(a, name, func(doc map[string]any) error {
			raw, err := json.Marshal(doc)
			if err != nil {
				return err
			}
			lead := "\n"
			if !first {
				lead = ",\n"
			}
			first = false
			rows++
			_, err = io.WriteString(w, lead+string(raw))
			return err
		})
		if err != nil {
			return rows, err
		}
		if _, err := io.WriteString(w, "]"); err != nil {
			return rows, err
		}
	}
	_, err := io.WriteString(w, "\n}}\n")
	return rows, err
}

// exportCSV writes collection as CSV and answers how many rows it wrote.
func (s *Service) exportCSV(w io.Writer, a *appCall, collection string) (int, error) {
	var cols appdata.CSVColumns
	if err := s.eachAppDoc(a, collection, func(doc map[string]any) error {
		cols.Add(doc)
		return nil
	}); err != nil {
		return 0, err
	}
	cw, err := appdata.NewCSVWriter(w, cols.Names())
	if err != nil {
		return 0, err
	}
	rows := 0
	err = s.eachAppDoc(a, collection, func(doc map[string]any) error {
		rows++
		return cw.Write(doc)
	})
	if ferr := cw.Flush(); err == nil {
		err = ferr
	}
	return rows, err
}
