// Package searchtest is an in-process fake of the OpenSearch REST subset
// internal/search uses, for tests: index lifecycle, _bulk, _search (bool
// filter of term/terms/range, a match_phrase over the same 2-character
// ngram analysis the real index uses, sort and search_after),
// _delete_by_query, _count and _refresh. It records every request so tests
// can assert request shapes, and can be told to fail.
package searchtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode"
)

// Request is one recorded request.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   []byte
	// User and Pass are the basic credentials sent ("" when none).
	User, Pass string
}

// Fake is the fake cluster. Lock-free accessors are safe while serving.
type Fake struct {
	*httptest.Server

	mu       sync.Mutex
	requests []Request
	indexes  map[string]*index
	// Fail, when set, answers every request matching it with its status.
	fail func(r *http.Request) int
	// IgnoreFilters makes _search ignore the bool filter (an index that
	// lies about scope), so tests can prove the store re-check.
	ignoreFilters bool
	// Credentials, when set, are required (401 otherwise).
	user, pass string
}

type index struct {
	definition json.RawMessage
	mapping    json.RawMessage
	docs       map[string]map[string]any
}

// New starts a fake cluster (closed with the test).
func New(t testing.TB) *Fake {
	f := &Fake{indexes: map[string]*index{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// RequireAuth makes the fake demand these basic credentials.
func (f *Fake) RequireAuth(user, pass string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.user, f.pass = user, pass
}

// FailWith makes every request match answers status (nil clears).
func (f *Fake) FailWith(match func(r *http.Request) int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = match
}

// IgnoreFilters makes _search return text matches from every scope.
func (f *Fake) IgnoreFilters(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ignoreFilters = on
}

// Requests returns a copy of the recorded requests.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// ResetRequests forgets the recorded requests.
func (f *Fake) ResetRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

// Definition returns the body an index was created with.
func (f *Fake) Definition(name string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ix := f.indexes[name]; ix != nil {
		return ix.definition
	}
	return nil
}

// Doc returns a stored document (nil when absent).
func (f *Fake) Doc(indexName, id string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ix := f.indexes[indexName]; ix != nil {
		return ix.docs[id]
	}
	return nil
}

// DocCount returns how many documents an index holds.
func (f *Fake) DocCount(indexName string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ix := f.indexes[indexName]; ix != nil {
		return len(ix.docs)
	}
	return 0
}

// PutDoc stores a document directly (to simulate a stale index).
func (f *Fake) PutDoc(indexName, id string, doc map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ix := f.indexes[indexName]; ix != nil {
		ix.docs[id] = doc
	}
}

// CreateIndexWithMapping creates an index with a given mapping (to simulate
// an existing incompatible index).
func (f *Fake) CreateIndexWithMapping(name string, properties map[string]any) {
	raw, _ := json.Marshal(map[string]any{"properties": properties})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.indexes[name] = &index{mapping: raw, docs: map[string]map[string]any{}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	user, pass, _ := r.BasicAuth()
	f.mu.Lock()
	f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body, User: user, Pass: pass})
	fail, wantUser, wantPass := f.fail, f.user, f.pass
	f.mu.Unlock()
	if wantUser != "" && (user != wantUser || pass != wantPass) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if fail != nil {
		if status := fail(r); status != 0 {
			writeJSON(w, status, map[string]any{"error": "injected failure"})
			return
		}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"version": map[string]any{"distribution": "opensearch", "number": "2.0.0-fake"}})
	case r.URL.Path == "/_bulk" && r.Method == http.MethodPost:
		f.bulk(w, body)
	case len(parts) == 3 && parts[0] == "_cluster" && parts[1] == "health":
		f.mu.Lock()
		_, ok := f.indexes[parts[2]]
		f.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "index_not_found_exception"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "green", "timed_out": false})
	case len(parts) == 1 && r.Method == http.MethodHead:
		f.mu.Lock()
		_, ok := f.indexes[parts[0]]
		f.mu.Unlock()
		if ok {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case len(parts) == 1 && r.Method == http.MethodPut:
		f.create(w, parts[0], body)
	case len(parts) == 2 && parts[1] == "_mapping" && r.Method == http.MethodGet:
		f.mu.Lock()
		ix, ok := f.indexes[parts[0]]
		f.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "index_not_found_exception"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{parts[0]: map[string]any{"mappings": ix.mapping}})
	case len(parts) == 2 && parts[1] == "_search" && r.Method == http.MethodPost:
		f.search(w, parts[0], body)
	case len(parts) == 2 && parts[1] == "_delete_by_query" && r.Method == http.MethodPost:
		f.deleteByQuery(w, parts[0], body)
	case len(parts) == 2 && parts[1] == "_count":
		writeJSON(w, http.StatusOK, map[string]any{"count": f.DocCount(parts[0])})
	case len(parts) == 2 && parts[1] == "_refresh":
		writeJSON(w, http.StatusOK, map[string]any{"_shards": map[string]any{"failed": 0}})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no handler for " + r.Method + " " + r.URL.Path})
	}
}

func (f *Fake) create(w http.ResponseWriter, name string, body []byte) {
	var def struct {
		Mappings json.RawMessage `json:"mappings"`
	}
	if err := json.Unmarshal(body, &def); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.indexes[name]; ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"type": "resource_already_exists_exception"}})
		return
	}
	f.indexes[name] = &index{definition: body, mapping: def.Mappings, docs: map[string]map[string]any{}}
	writeJSON(w, http.StatusOK, map[string]any{"acknowledged": true, "index": name})
}

func (f *Fake) bulk(w http.ResponseWriter, body []byte) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var items []map[string]any
	errors := false
	for sc.Scan() {
		var meta map[string]struct {
			Index string `json:"_index"`
			ID    string `json:"_id"`
		}
		if err := json.Unmarshal(sc.Bytes(), &meta); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad bulk line"})
			return
		}
		for op, m := range meta {
			f.mu.Lock()
			ix := f.indexes[m.Index]
			f.mu.Unlock()
			switch op {
			case "index":
				if !sc.Scan() {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing source"})
					return
				}
				var doc map[string]any
				_ = json.Unmarshal(sc.Bytes(), &doc)
				if ix == nil {
					errors = true
					items = append(items, map[string]any{op: map[string]any{"status": 404, "error": "index_not_found_exception"}})
					continue
				}
				f.mu.Lock()
				ix.docs[m.ID] = doc
				f.mu.Unlock()
				items = append(items, map[string]any{op: map[string]any{"status": 201}})
			case "delete":
				status := 404
				if ix != nil {
					f.mu.Lock()
					if _, ok := ix.docs[m.ID]; ok {
						delete(ix.docs, m.ID)
						status = 200
					}
					f.mu.Unlock()
				}
				items = append(items, map[string]any{op: map[string]any{"status": status}})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"errors": errors, "items": items})
}

// query is the subset of the query DSL internal/search sends.
type query struct {
	Size        int             `json:"size"`
	SearchAfter []float64       `json:"search_after"`
	Query       json.RawMessage `json:"query"`
}

type boolQuery struct {
	Bool struct {
		Filter []map[string]map[string]json.RawMessage `json:"filter"`
		Must   []map[string]map[string]string          `json:"must"`
	} `json:"bool"`
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

// matchesFilter evaluates one filter clause against a document.
func matchesFilter(doc map[string]any, clause map[string]map[string]json.RawMessage) bool {
	for typ, fields := range clause {
		for field, raw := range fields {
			switch typ {
			case "term":
				var want any
				_ = json.Unmarshal(raw, &want)
				if doc[field] != want {
					return false
				}
			case "terms":
				var want []any
				_ = json.Unmarshal(raw, &want)
				ok := false
				for _, v := range want {
					if doc[field] == v {
						ok = true
					}
				}
				if !ok {
					return false
				}
			case "range":
				var r map[string]float64
				_ = json.Unmarshal(raw, &r)
				v := num(doc[field])
				if lt, ok := r["lt"]; ok && !(v < lt) {
					return false
				}
			}
		}
	}
	return true
}

// Analyze is the real index's text analysis: 2-character ngrams of each
// run of non-space characters, lowercased, positions consecutive.
func Analyze(text string) []string {
	var out []string
	for _, word := range strings.FieldsFunc(strings.ToLower(text), unicode.IsSpace) {
		r := []rune(word)
		for i := 0; i+2 <= len(r); i++ {
			out = append(out, string(r[i:i+2]))
		}
	}
	return out
}

// phraseMatch reports whether q's tokens occur consecutively in text's.
func phraseMatch(text, q string) bool {
	doc, want := Analyze(text), Analyze(q)
	if len(want) == 0 {
		return false
	}
	for i := 0; i+len(want) <= len(doc); i++ {
		ok := true
		for j := range want {
			if doc[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (f *Fake) search(w http.ResponseWriter, name string, body []byte) {
	var q query
	var bq boolQuery
	if err := json.Unmarshal(body, &q); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	_ = json.Unmarshal(q.Query, &bq)
	f.mu.Lock()
	ix, ok := f.indexes[name]
	ignore := f.ignoreFilters
	var docs []map[string]any
	if ok {
		for _, d := range ix.docs {
			docs = append(docs, d)
		}
	}
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "index_not_found_exception"})
		return
	}
	var hits []map[string]any
	for _, d := range docs {
		pass := true
		if !ignore {
			for _, c := range bq.Bool.Filter {
				if !matchesFilter(d, c) {
					pass = false
					break
				}
			}
		}
		for _, m := range bq.Bool.Must {
			text, _ := d["text"].(string)
			if !phraseMatch(text, m["match_phrase"]["text"]) {
				pass = false
			}
		}
		if pass {
			hits = append(hits, d)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		ci, cj := num(hits[i]["createdAt"]), num(hits[j]["createdAt"])
		if ci != cj {
			return ci > cj
		}
		return num(hits[i]["seq"]) > num(hits[j]["seq"])
	})
	if len(q.SearchAfter) == 2 {
		after := hits[:0:0]
		for _, h := range hits {
			c, s := num(h["createdAt"]), num(h["seq"])
			if c < q.SearchAfter[0] || (c == q.SearchAfter[0] && s < q.SearchAfter[1]) {
				after = append(after, h)
			}
		}
		hits = after
	}
	if q.Size > 0 && len(hits) > q.Size {
		hits = hits[:q.Size]
	}
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"_source": map[string]any{"tenant": h["tenant"], "room": h["room"], "seq": h["seq"]},
			"sort":    []any{h["createdAt"], h["seq"]},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": map[string]any{"hits": out}})
}

func (f *Fake) deleteByQuery(w http.ResponseWriter, name string, body []byte) {
	var bq struct {
		Query boolQuery `json:"query"`
	}
	_ = json.Unmarshal(body, &bq)
	f.mu.Lock()
	defer f.mu.Unlock()
	ix, ok := f.indexes[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "index_not_found_exception"})
		return
	}
	deleted := 0
	for id, d := range ix.docs {
		pass := true
		for _, c := range bq.Query.Bool.Filter {
			if !matchesFilter(d, c) {
				pass = false
			}
		}
		if pass {
			delete(ix.docs, id)
			deleted++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "failures": []any{}})
}
