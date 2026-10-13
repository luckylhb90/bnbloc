package room

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// auditSink records the audit rows the room service writes.
type auditSink struct {
	mu   sync.Mutex
	recs []store.ExtAuditRecord
}

func (s *auditSink) AppendExtAudit(_ context.Context, rec store.ExtAuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, rec)
	return nil
}

// take returns the rows recorded since the last take.
func (s *auditSink) take() []store.ExtAuditRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.recs
	s.recs = nil
	return out
}

func (e *appEnv) audited() *auditSink {
	sink := &auditSink{}
	e.svc.Audit = sink
	return sink
}

// An export is the owners' only: it streams every collection as JSON (or
// the one named), and each export writes exactly one audit row naming the
// actor, the app, the collection, the format and the row count.
func TestAppDataExportOwnersOnlyAndAudited(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		sink := e.audited()
		e.insert(maya, "votes", map[string]any{"choice": "A"}, false)
		e.insert(jack, "votes", map[string]any{"choice": "B"}, false)
		e.insert(visitA, "votes", map[string]any{"choice": "C"}, false)
		e.insert(visitA, "notes", map[string]any{"text": "hi"}, true)
		sink.take()

		r := e.do(jack, http.MethodGet, "/export?format=json", nil)
		var out struct {
			Collections map[string][]map[string]any `json:"collections"`
		}
		if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil {
			t.Fatalf("export = %d %s", r.status, r.body)
		}
		if len(out.Collections["votes"]) != 3 || len(out.Collections["notes"]) != 1 || out.Collections["votes"][2]["_author"] == nil {
			t.Fatalf("export holds %s", r.body)
		}
		recs := sink.take()
		if len(recs) != 1 {
			t.Fatalf("an export wrote %d audit rows, want 1: %+v", len(recs), recs)
		}
		got := recs[0]
		if got.Kind != wire.AuditKindApp || got.Name != e.app || got.Action != "export" || got.Actor != "jack" || got.TenantID != e.tenant ||
			got.Outcome != wire.AuditOutcomeOK || !hasFields(got.Detail, "collection=*", "rows=4", "format=json", "room="+e.room) {
			t.Fatalf("export audit row = %+v", got)
		}

		// One collection.
		r = e.do(maya, http.MethodGet, "/export?collection=notes", nil)
		out.Collections = nil
		if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil || len(out.Collections) != 1 || len(out.Collections["notes"]) != 1 {
			t.Fatalf("export of notes = %d %s", r.status, r.body)
		}
		if recs := sink.take(); len(recs) != 1 || !hasFields(recs[0].Detail, "collection=notes", "rows=1") || recs[0].Actor != "maya" {
			t.Fatalf("export of notes audit = %+v", recs)
		}

		// Not an owner: no export, no row.
		for _, p := range []who{vic, visitA, zed} {
			if r := e.do(p, http.MethodGet, "/export", nil); r.status != http.StatusForbidden && r.status != http.StatusNotFound {
				t.Errorf("export as %v = %d %s", p, r.status, r.body)
			}
		}
		// Malformed requests.
		for _, q := range []string{"?format=xml", "?format=csv", "?collection=a/b", "?collection=..&format=csv"} {
			if r := e.do(maya, http.MethodGet, "/export"+q, nil); r.status != http.StatusBadRequest {
				t.Errorf("export%s = %d %s", q, r.status, r.body)
			}
		}
		if recs := sink.take(); len(recs) != 0 {
			t.Fatalf("refused exports were audited: %+v", recs)
		}
	})
}

// A CSV export is one collection: the stamps first, then the fields in
// order of first appearance, nested values as JSON text, every cell
// escaped (quotes, commas, line breaks) and formula-like text defused.
func TestAppDataExportCSVEscaped(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		sink := e.audited()
		e.insert(maya, "rows", map[string]any{"name": "=SUM(A1)", "note": `say "hi", then` + "\nleave", "n": 3.5, "ok": true}, false)
		e.insert(jack, "rows", map[string]any{"name": "plain", "tags": []any{"a", "b"}, "meta": map[string]any{"x": 1}, "n": -2}, false)
		sink.take()

		r := e.do(maya, http.MethodGet, "/export?collection=rows&format=csv", nil)
		if r.status != http.StatusOK {
			t.Fatalf("csv export = %d %s", r.status, r.body)
		}
		if !strings.HasSuffix(string(r.body), "\r\n") || strings.Contains(strings.ReplaceAll(string(r.body), "\r\n", ""), "\n") {
			t.Fatalf("csv lines are not CRLF-terminated: %q", r.body)
		}
		rd := csv.NewReader(strings.NewReader(string(r.body)))
		records, err := rd.ReadAll()
		if err != nil || len(records) != 3 {
			t.Fatalf("csv = %v %q", err, r.body)
		}
		// Rows are in _id order (two inserts in one millisecond in
		// either); the fields follow the rows, each row's in name order.
		header := records[0]
		first, second := 1, 2
		wantHeader := []string{"_id", "_by", "_author", "_createdAt", "_updatedAt", "_version", "n", "name", "note", "ok", "meta", "tags"}
		if slices.Contains(records[1], "plain") {
			first, second = 2, 1
			wantHeader = []string{"_id", "_by", "_author", "_createdAt", "_updatedAt", "_version", "meta", "n", "name", "tags", "note", "ok"}
		}
		if !slices.Equal(header, wantHeader) {
			t.Fatalf("header = %v, want %v", header, wantHeader)
		}
		cell := func(row int, col string) string { return records[row][slices.Index(header, col)] }
		for _, c := range []struct {
			row       int
			col, want string
		}{
			{first, "name", "'=SUM(A1)"},
			{first, "note", `say "hi", then` + "\nleave"},
			{first, "n", "3.5"},
			{first, "ok", "true"},
			{first, "tags", ""},
			{second, "name", "plain"},
			{second, "n", "-2"},
			{second, "tags", `["a","b"]`},
			{second, "meta", `{"x":1}`},
			{second, "_author", `{"id":"jack","kind":"member"}`},
		} {
			if got := cell(c.row, c.col); got != c.want {
				t.Errorf("row %d %s = %q, want %q", c.row, c.col, got, c.want)
			}
		}
		if !strings.Contains(string(r.body), `"say ""hi"", then`) {
			t.Errorf("quotes are not doubled inside a quoted cell: %q", r.body)
		}
		if recs := sink.take(); len(recs) != 1 || !hasFields(recs[0].Detail, "collection=rows", "rows=2", "format=csv") {
			t.Fatalf("csv export audit = %+v", recs)
		}
	})
}

// An agent's reads of documents through its job's grant are audited, one
// row per call (not per document), naming the agent and its owner; a
// member's own reads through the app are not, nor are an agent's reads of
// no document (me, stats).
func TestAppDataAgentReadsAudited(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		sink := e.audited()
		id := e.insert(jack, "votes", map[string]any{"choice": "A"}, false)["_id"].(string)
		e.insert(maya, "votes", map[string]any{"choice": "B"}, false)
		e.insert(jack, "votes", map[string]any{"choice": "C"}, true)

		// A member's own reads (a member ticket): no row.
		e.find(jack, "votes", "")
		if r := e.do(jack, http.MethodGet, "/c/votes/"+id, nil); r.status != http.StatusOK {
			t.Fatalf("member get = %d %s", r.status, r.body)
		}
		e.find(visitA, "votes", "")
		e.me(agent)
		if r := e.do(agent, http.MethodGet, "/stats", nil); r.status != http.StatusOK {
			t.Fatalf("agent stats = %d %s", r.status, r.body)
		}
		if recs := sink.take(); len(recs) != 0 {
			t.Fatalf("reads that are not an agent's documents were audited: %+v", recs)
		}

		// An agent's find: one row for the page of three.
		if p := e.find(agent, "votes", ""); len(p.Docs) != 3 {
			t.Fatalf("agent find = %d docs", len(p.Docs))
		}
		recs := sink.take()
		if len(recs) != 1 {
			t.Fatalf("an agent find wrote %d audit rows, want 1: %+v", len(recs), recs)
		}
		got := recs[0]
		if got.Kind != wire.AuditKindApp || got.Name != e.app || got.Action != "find" || got.Actor != "maya" || got.TenantID != e.tenant ||
			!hasFields(got.Detail, "agent=maya/codex", "collection=votes", "rows=3", "room="+e.room) {
			t.Fatalf("agent find audit row = %+v", got)
		}
		// An agent's get: one row.
		if r := e.do(agent, http.MethodGet, "/c/votes/"+id, nil); r.status != http.StatusOK {
			t.Fatalf("agent get = %d %s", r.status, r.body)
		}
		if recs := sink.take(); len(recs) != 1 || recs[0].Action != "get" || !hasFields(recs[0].Detail, "rows=1", "agent=maya/codex") {
			t.Fatalf("agent get audit = %+v", recs)
		}
	})
}

// hasFields reports whether a detail line holds every field.
func hasFields(detail string, fields ...string) bool {
	have := strings.Fields(detail)
	for _, f := range fields {
		if !slices.Contains(have, f) {
			return false
		}
	}
	return true
}
