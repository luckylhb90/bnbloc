package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// OpenSearch is the external Index: plain REST over net/http, no client
// SDK. One index holds every tenant's room messages, one document per
// stored message keyed by (tenant, room, seq).
//
// Analysis. Room messages mix CJK (no word delimiters) and English, and
// the MySQL path matches substrings (an ngram phrase query). The index
// reproduces that with components of the standard distribution only (no
// plugin): an ngram tokenizer of exactly 2 characters over letters, digits,
// punctuation and symbols (whitespace splits, like MySQL's ngram parser),
// then cjk_width (full-width Latin and half-width katakana fold to their
// normal forms), lowercase and asciifolding (case- and accent-insensitive,
// like utf8mb4_0900_ai_ci). A match_phrase query is then the consecutive
// bigram sequence: a substring match for every query of at least two
// characters, exactly the MySQL semantics. The built-in cjk analyzer was
// rejected: it bigrams CJK but keeps English as whole words, so "ploy"
// would not find "deploy" while MySQL does. The ICU and smartcn analyzers
// need plugins.
//
// Documents carry the scope fields (tenant, room, channel, channelKind,
// author, kind, createdAt, deleted) so filters run inside the query; the
// store re-checks every candidate (store.MessageSearch.Admits).
type OpenSearch struct {
	base    *url.URL
	user    string
	pass    string
	index   string
	http    *http.Client
	src     Source
	metrics *obs.Metrics

	ready    atomic.Bool
	ensureMu sync.Mutex
}

// Source reads stored rows for the index (store.ChatIndex satisfies it).
type Source interface {
	LoadSearchSources(ctx context.Context, keys []store.MessageKey) ([]store.SearchSource, error)
	ScanSearchSources(ctx context.Context, cursor string, batch int) ([]store.SearchSource, store.SearchReindexBatch, error)
}

// OpenSearchConfig configures the OpenSearch index.
type OpenSearchConfig struct {
	// URL is the cluster base URL (http or https, optionally with a path
	// prefix when a proxy fronts the cluster). No credentials in it.
	URL string
	// Username and Password are HTTP basic credentials; both or neither.
	Username string
	Password string
	// IndexPrefix names the index: <prefix>-room-messages-v<N>.
	IndexPrefix string
	// HTTP is the client to use (nil = a default client; every call is
	// bounded by its context).
	HTTP *http.Client
}

// DefaultIndexPrefix is the index name prefix when none is configured.
const DefaultIndexPrefix = "bnbloc"

// indexSchemaVersion is the mapping version, part of the index name. A
// mapping or analyzer change bumps it: the new index is created empty at
// startup and filled by `bnbloc search reindex`, and the old one is
// deleted by the operator once the new one is complete.
const indexSchemaVersion = 1

// textAnalyzer is the custom analyzer of the text field.
const textAnalyzer = "bnbloc_text"

// Call budgets. Each OpenSearch call runs under the caller's context and at
// most this long.
const (
	searchCallTimeout = 10 * time.Second
	writeCallTimeout  = 30 * time.Second
	purgeCallTimeout  = 10 * time.Minute
	probeCallTimeout  = 10 * time.Second
)

// maxBulkBytes bounds one _bulk request body (agent replies may be large).
const maxBulkBytes = 8 << 20

// maxSearchRounds bounds the extra pages a search reads when the store
// rejects stale candidates of a full page.
const maxSearchRounds = 3

var indexPrefixRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

// NewOpenSearch validates cfg and returns the index over src. It does not
// contact the cluster; EnsureIndex does.
func NewOpenSearch(cfg OpenSearchConfig, src Source, metrics *obs.Metrics) (*OpenSearch, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("search: OpenSearch URL %q is not an http(s) URL", cfg.URL)
	}
	if u.User != nil {
		return nil, errors.New("search: put the OpenSearch credentials in the username/password settings, not in the URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("search: OpenSearch URL %q must not carry a query or fragment", cfg.URL)
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, errors.New("search: OpenSearch basic auth needs both a username and a password")
	}
	prefix := cfg.IndexPrefix
	if prefix == "" {
		prefix = DefaultIndexPrefix
	}
	if !indexPrefixRe.MatchString(prefix) {
		return nil, fmt.Errorf("search: OpenSearch index prefix %q must be lowercase letters, digits, '.', '_' or '-', starting with a letter or digit", prefix)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	client := cfg.HTTP
	if client == nil {
		client = &http.Client{}
	}
	return &OpenSearch{
		base: u, user: cfg.Username, pass: cfg.Password,
		index:   prefix + "-room-messages-v" + strconv.Itoa(indexSchemaVersion),
		http:    client,
		src:     src,
		metrics: metrics,
	}, nil
}

var _ Index = (*OpenSearch)(nil)

// Backend is BackendOpenSearch.
func (o *OpenSearch) Backend() string { return BackendOpenSearch }

// IndexName is the index this instance reads and writes.
func (o *OpenSearch) IndexName() string { return o.index }

// --- transport ---

// statusError is a non-2xx answer.
type statusError struct {
	op     string
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("opensearch %s: status %d: %s", e.op, e.status, e.body)
}

// do sends one request and returns the response body of a 2xx answer, or a
// *statusError. body is JSON unless contentType says otherwise.
func (o *OpenSearch) do(ctx context.Context, timeout time.Duration, op, method, path string, query url.Values, body []byte, contentType string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := *o.base
	u.Path = o.base.Path + path
	u.RawQuery = query.Encode()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if o.user != "" {
		req.SetBasicAuth(o.user, o.pass)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opensearch %s: %w", op, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("opensearch %s: read: %w", op, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 512 {
			msg = msg[:512] + "…"
		}
		return nil, &statusError{op: op, status: resp.StatusCode, body: msg}
	}
	return out, nil
}

func (o *OpenSearch) doJSON(ctx context.Context, timeout time.Duration, op, method, path string, query url.Values, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	raw, err := o.do(ctx, timeout, op, method, path, query, body, "")
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("opensearch %s: decode: %w", op, err)
	}
	return nil
}

func hasStatus(err error, status int) bool {
	var se *statusError
	return errors.As(err, &se) && se.status == status
}

// --- index lifecycle ---

// indexDefinition is the explicit index body: analysis and a strict
// mapping. Shard and replica counts are a property of the cluster, not of
// bnbloc: they are left to the cluster defaults or to an index template
// the operator installs for <prefix>-*.
func indexDefinition() map[string]any {
	keyword := map[string]any{"type": "keyword"}
	return map[string]any{
		"settings": map[string]any{
			"analysis": map[string]any{
				"tokenizer": map[string]any{
					"bnbloc_bigram": map[string]any{
						"type": "ngram", "min_gram": 2, "max_gram": 2,
						"token_chars": []string{"letter", "digit", "punctuation", "symbol"},
					},
				},
				"analyzer": map[string]any{
					textAnalyzer: map[string]any{
						"type": "custom", "tokenizer": "bnbloc_bigram",
						"filter": []string{"cjk_width", "lowercase", "asciifolding"},
					},
				},
			},
		},
		"mappings": map[string]any{
			"dynamic": "strict",
			"properties": map[string]any{
				"tenant":      keyword,
				"room":        keyword,
				"channel":     keyword,
				"channelKind": keyword,
				"seq":         map[string]any{"type": "long"},
				"kind":        keyword,
				"author":      keyword,
				"text":        map[string]any{"type": "text", "analyzer": textAnalyzer},
				"createdAt":   map[string]any{"type": "date", "format": "epoch_millis"},
				"editedAt":    map[string]any{"type": "date", "format": "epoch_millis"},
				"deleted":     map[string]any{"type": "boolean"},
			},
		},
	}
}

// requiredFields are the mapped fields queries and writes rely on, with
// their type.
var requiredFields = map[string]string{
	"tenant": "keyword", "room": "keyword", "channel": "keyword", "channelKind": "keyword",
	"seq": "long", "kind": "keyword", "author": "keyword", "text": "text",
	"createdAt": "date", "editedAt": "date", "deleted": "boolean",
}

// EnsureIndex checks that the cluster is reachable and accepts the
// credentials, creates the index when it is missing (a concurrent creator
// is fine), verifies an existing index's mapping, and waits until the index
// is allocated (yellow or green). Every Index call runs it first until it
// succeeds once.
func (o *OpenSearch) EnsureIndex(ctx context.Context) error {
	o.ensureMu.Lock()
	defer o.ensureMu.Unlock()
	if o.ready.Load() {
		return nil
	}
	if _, err := o.do(ctx, probeCallTimeout, "probe", http.MethodGet, "/", nil, nil, ""); err != nil {
		if hasStatus(err, http.StatusUnauthorized) || hasStatus(err, http.StatusForbidden) {
			return fmt.Errorf("opensearch refused the configured credentials: %w", err)
		}
		return err
	}
	_, err := o.do(ctx, probeCallTimeout, "index exists", http.MethodHead, "/"+o.index, nil, nil, "")
	switch {
	case hasStatus(err, http.StatusNotFound):
		err = o.doJSON(ctx, probeCallTimeout, "create index", http.MethodPut, "/"+o.index, nil, indexDefinition(), nil)
		if err != nil && !strings.Contains(err.Error(), "resource_already_exists_exception") {
			return err
		}
		if err != nil {
			// Another replica created it first: verify what it created.
			if err := o.verifyMapping(ctx); err != nil {
				return err
			}
		}
	case err != nil:
		return err
	default:
		if err := o.verifyMapping(ctx); err != nil {
			return err
		}
	}
	var health struct {
		Status   string `json:"status"`
		TimedOut bool   `json:"timed_out"`
	}
	q := url.Values{"wait_for_status": {"yellow"}, "timeout": {"5s"}}
	if err := o.doJSON(ctx, probeCallTimeout, "index health", http.MethodGet, "/_cluster/health/"+o.index, q, nil, &health); err != nil {
		return err
	}
	if health.TimedOut || health.Status == "red" {
		return fmt.Errorf("opensearch index %s is not allocated (health %s)", o.index, health.Status)
	}
	o.ready.Store(true)
	return nil
}

// verifyMapping refuses an existing index whose mapping differs from what
// this version writes and queries (wrong field types or text analyzer).
func (o *OpenSearch) verifyMapping(ctx context.Context) error {
	var got map[string]struct {
		Mappings struct {
			Properties map[string]struct {
				Type     string `json:"type"`
				Analyzer string `json:"analyzer"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if err := o.doJSON(ctx, probeCallTimeout, "get mapping", http.MethodGet, "/"+o.index+"/_mapping", nil, nil, &got); err != nil {
		return err
	}
	for name, idx := range got {
		for field, typ := range requiredFields {
			p, ok := idx.Mappings.Properties[field]
			if !ok || p.Type != typ {
				return fmt.Errorf("opensearch index %s (%s) maps %q as %q, want %q: delete it or choose another index prefix, then run `bnbloc search reindex`", o.index, name, field, p.Type, typ)
			}
		}
		if a := idx.Mappings.Properties["text"].Analyzer; a != textAnalyzer {
			return fmt.Errorf("opensearch index %s (%s) analyzes text with %q, want %q: delete it or choose another index prefix, then run `bnbloc search reindex`", o.index, name, a, textAnalyzer)
		}
	}
	return nil
}

func (o *OpenSearch) ensure(ctx context.Context) error {
	if o.ready.Load() {
		return nil
	}
	return o.EnsureIndex(ctx)
}

// --- documents ---

// document is one indexed room message. A soft-deleted message keeps its
// document with deleted=true and no text: the scope filter excludes it and
// the index holds no deleted content.
type document struct {
	Tenant      string `json:"tenant"`
	Room        string `json:"room"`
	Channel     string `json:"channel"`
	ChannelKind string `json:"channelKind"`
	Seq         int64  `json:"seq"`
	Kind        string `json:"kind"`
	Author      string `json:"author"`
	Text        string `json:"text,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	EditedAt    *int64 `json:"editedAt,omitempty"`
	Deleted     bool   `json:"deleted"`
}

// docID is the document id of a message: its escaped tenant, room and
// sequence (escaping keeps the three parts unambiguous).
func docID(tenantID, roomID string, seq int64) string {
	return url.PathEscape(tenantID) + "/" + url.PathEscape(roomID) + "/" + strconv.FormatInt(seq, 10)
}

// documentOf returns the document of a stored row, or ok=false when the row
// has no searchable text at all (a retention fence, an attachment-only
// message, another kind): such a row has no document.
func documentOf(src store.SearchSource) (document, bool) {
	if store.MessageText(src.Kind, src.Payload) == "" {
		return document{}, false
	}
	return document{
		Tenant: src.TenantID, Room: src.RoomID, Channel: src.ChannelID, ChannelKind: src.ChannelKind,
		Seq: src.Seq, Kind: src.Kind, Author: store.MessageAuthor(src.RoomMessage),
		Text: src.Text(), CreatedAt: src.CreatedAtMs, EditedAt: src.EditedAtMs, Deleted: src.Deleted,
	}, true
}

// bulk accumulates _bulk operations and sends them in bounded requests.
type bulk struct {
	o       *OpenSearch
	ctx     context.Context
	buf     bytes.Buffer
	ops     int
	indexed int
	failed  int
	first   string
}

func (b *bulk) index(id string, doc document) error {
	meta, _ := json.Marshal(map[string]any{"index": map[string]any{"_index": b.o.index, "_id": id}})
	src, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return b.add(meta, src)
}

func (b *bulk) delete(id string) error {
	meta, _ := json.Marshal(map[string]any{"delete": map[string]any{"_index": b.o.index, "_id": id}})
	return b.add(meta, nil)
}

func (b *bulk) add(lines ...[]byte) error {
	var n int
	for _, l := range lines {
		if l != nil {
			n += len(l) + 1
		}
	}
	if b.ops > 0 && b.buf.Len()+n > maxBulkBytes {
		if err := b.flush(b.ctx); err != nil {
			return err
		}
	}
	for _, l := range lines {
		if l != nil {
			b.buf.Write(l)
			b.buf.WriteByte('\n')
		}
	}
	b.ops++
	return nil
}

// flush sends the pending operations. A transport or request failure is
// returned; per-item failures are counted (a delete of a missing document
// is not a failure).
func (b *bulk) flush(ctx context.Context) error {
	if b.ops == 0 {
		return nil
	}
	body := append([]byte(nil), b.buf.Bytes()...)
	b.buf.Reset()
	b.ops = 0
	raw, err := b.o.do(ctx, writeCallTimeout, "bulk", http.MethodPost, "/_bulk", nil, body, "application/x-ndjson")
	if err != nil {
		return err
	}
	var res struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int             `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("opensearch bulk: decode: %w", err)
	}
	for _, item := range res.Items {
		for op, r := range item {
			switch {
			case r.Status >= 200 && r.Status <= 299:
				if op == "index" {
					b.indexed++
				}
			case op == "delete" && r.Status == http.StatusNotFound:
			default:
				b.failed++
				if b.first == "" {
					b.first = fmt.Sprintf("%s status %d: %s", op, r.Status, r.Error)
				}
			}
		}
	}
	return nil
}

// finish flushes and reports item failures as one error.
func (b *bulk) finish(ctx context.Context) error {
	if err := b.flush(ctx); err != nil {
		return err
	}
	if b.failed > 0 {
		return fmt.Errorf("opensearch bulk: %d operations failed, first: %s", b.failed, b.first)
	}
	return nil
}

// write adds the operation that makes src's document current.
func (b *bulk) write(src store.SearchSource) error {
	id := docID(src.TenantID, src.RoomID, src.Seq)
	if doc, ok := documentOf(src); ok {
		return b.index(id, doc)
	}
	return b.delete(id)
}

// Apply re-reads keys from the store and writes their current state: a
// document per row with text, a delete for a row that is gone or has none.
func (o *OpenSearch) Apply(ctx context.Context, keys []store.MessageKey) error {
	if len(keys) == 0 {
		return nil
	}
	if err := o.ensure(ctx); err != nil {
		return err
	}
	srcs, err := o.src.LoadSearchSources(ctx, keys)
	if err != nil {
		return fmt.Errorf("load messages: %w", err)
	}
	found := make(map[store.MessageKey]store.SearchSource, len(srcs))
	for _, s := range srcs {
		found[store.MessageKey{TenantID: s.TenantID, RoomID: s.RoomID, Seq: s.Seq}] = s
	}
	b := &bulk{o: o, ctx: ctx}
	for _, k := range keys {
		var err error
		if s, ok := found[k]; ok {
			err = b.write(s)
		} else {
			err = b.delete(docID(k.TenantID, k.RoomID, k.Seq))
		}
		if err != nil {
			return err
		}
	}
	return b.finish(ctx)
}

// Reindex writes the current state of one batch of the feed. Updated is
// the number of documents written.
func (o *OpenSearch) Reindex(ctx context.Context, cursor string, batch int) (store.SearchReindexBatch, error) {
	srcs, out, err := o.src.ScanSearchSources(ctx, cursor, batch)
	if err != nil {
		return store.SearchReindexBatch{}, err
	}
	if err := o.ensure(ctx); err != nil {
		return store.SearchReindexBatch{}, err
	}
	b := &bulk{o: o, ctx: ctx}
	for _, s := range srcs {
		if err := b.write(s); err != nil {
			return store.SearchReindexBatch{}, err
		}
	}
	if err := b.finish(ctx); err != nil {
		return store.SearchReindexBatch{}, err
	}
	out.Updated = b.indexed
	return out, nil
}

// PurgeBefore deletes the tenant's documents created before beforeMs with
// one delete-by-query (it may run for a while on a large index).
func (o *OpenSearch) PurgeBefore(ctx context.Context, tenantID string, beforeMs int64) (int64, error) {
	if err := o.ensure(ctx); err != nil {
		return 0, err
	}
	body := map[string]any{"query": map[string]any{"bool": map[string]any{"filter": []any{
		map[string]any{"term": map[string]any{"tenant": tenantID}},
		map[string]any{"range": map[string]any{"createdAt": map[string]any{"lt": beforeMs}}},
	}}}}
	q := url.Values{"conflicts": {"proceed"}, "wait_for_completion": {"true"}, "slices": {"auto"}}
	var res struct {
		Deleted  int64             `json:"deleted"`
		Failures []json.RawMessage `json:"failures"`
	}
	if err := o.doJSON(ctx, purgeCallTimeout, "delete by query", http.MethodPost, "/"+o.index+"/_delete_by_query", q, body, &res); err != nil {
		return 0, err
	}
	if len(res.Failures) > 0 {
		return res.Deleted, fmt.Errorf("opensearch delete by query: %d failures, first: %s", len(res.Failures), res.Failures[0])
	}
	return res.Deleted, nil
}

// --- query ---

// hit is one candidate of a search page.
type hit struct {
	key  store.MessageKey
	sort []json.RawMessage // passed back verbatim as search_after
}

// query returns one page of candidates, newest first, after the sort
// values of the previous page's last hit.
func (o *OpenSearch) query(ctx context.Context, tenantID string, q store.MessageSearch, size int, after []json.RawMessage) ([]hit, error) {
	filter := []any{
		map[string]any{"term": map[string]any{"tenant": tenantID}},
		map[string]any{"terms": map[string]any{"room": q.RoomIDs}},
		map[string]any{"term": map[string]any{"deleted": false}},
	}
	if q.BeforeMs > 0 {
		filter = append(filter, map[string]any{"range": map[string]any{"createdAt": map[string]any{"lt": q.BeforeMs}}})
	}
	body := map[string]any{
		"size":             size,
		"track_total_hits": false,
		"_source":          []string{"tenant", "room", "seq"},
		"query": map[string]any{"bool": map[string]any{
			"filter": filter,
			"must":   []any{map[string]any{"match_phrase": map[string]any{"text": strings.TrimSpace(q.Text)}}},
		}},
		"sort": []any{
			map[string]any{"createdAt": map[string]any{"order": "desc"}},
			map[string]any{"seq": map[string]any{"order": "desc"}},
		},
	}
	if after != nil {
		body["search_after"] = after
	}
	var res struct {
		Hits struct {
			Hits []struct {
				Source struct {
					Tenant string `json:"tenant"`
					Room   string `json:"room"`
					Seq    int64  `json:"seq"`
				} `json:"_source"`
				Sort []json.RawMessage `json:"sort"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := o.doJSON(ctx, searchCallTimeout, "search", http.MethodPost, "/"+o.index+"/_search", nil, body, &res); err != nil {
		return nil, err
	}
	out := make([]hit, 0, len(res.Hits.Hits))
	for _, h := range res.Hits.Hits {
		out = append(out, hit{key: store.MessageKey{TenantID: h.Source.Tenant, RoomID: h.Source.Room, Seq: h.Source.Seq}, sort: h.Sort})
	}
	return out, nil
}

// Search asks the index for candidates and returns those the store admits
// (store.MessageSearch.Admits), in the index's order. A candidate the store
// rejects is stale (an update not yet applied, or lost); when a full page
// loses some, the next page fills the gap, a bounded number of times.
func (o *OpenSearch) Search(ctx context.Context, q store.MessageSearch) ([]store.RoomMessage, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if !q.Searchable() {
		return nil, nil
	}
	if err := o.ensure(ctx); err != nil {
		return nil, err
	}
	size := q.PageSize()
	var out []store.RoomMessage
	var after []json.RawMessage
	for round := 0; round < maxSearchRounds && len(out) < size; round++ {
		hits, err := o.query(ctx, tid, q, size, after)
		if err != nil {
			return nil, err
		}
		if len(hits) == 0 {
			break
		}
		keys := make([]store.MessageKey, len(hits))
		for i, h := range hits {
			keys[i] = h.key
		}
		srcs, err := o.src.LoadSearchSources(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("load candidates: %w", err)
		}
		byKey := make(map[store.MessageKey]store.SearchSource, len(srcs))
		for _, s := range srcs {
			byKey[store.MessageKey{TenantID: s.TenantID, RoomID: s.RoomID, Seq: s.Seq}] = s
		}
		stale := 0
		for _, h := range hits {
			s, ok := byKey[h.key]
			if !ok || !q.Admits(tid, s) {
				stale++
				continue
			}
			m := s.RoomMessage
			m.TenantID = "" // store reads leave it empty (store.RoomMessage)
			out = append(out, m)
		}
		o.metrics.CounterAdd(MetricStaleCandidates, float64(stale))
		if len(hits) < size || stale == 0 {
			break
		}
		after = hits[len(hits)-1].sort
	}
	if len(out) > size {
		out = out[:size]
	}
	return out, nil
}

// --- status ---

// Status reports reachability, the index's health and document count.
func (o *OpenSearch) Status(ctx context.Context) wire.SearchStatus {
	st := wire.SearchStatus{Backend: BackendOpenSearch, Index: o.index}
	if err := o.ensure(ctx); err != nil {
		st.Error = err.Error()
		return st
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := o.doJSON(ctx, probeCallTimeout, "index health", http.MethodGet, "/_cluster/health/"+o.index, nil, nil, &health); err != nil {
		st.Error = err.Error()
		return st
	}
	st.Health = health.Status
	var count struct {
		Count int64 `json:"count"`
	}
	if err := o.doJSON(ctx, probeCallTimeout, "count", http.MethodGet, "/"+o.index+"/_count", nil, nil, &count); err != nil {
		st.Error = err.Error()
		return st
	}
	st.Documents = &count.Count
	st.Ready = health.Status != "red"
	return st
}

// Refresh makes every write so far visible to search. The cluster
// refreshes on its own interval (1 s by default); tests call it to read
// their writes at once.
func (o *OpenSearch) Refresh(ctx context.Context) error {
	_, err := o.do(ctx, writeCallTimeout, "refresh", http.MethodPost, "/"+o.index+"/_refresh", nil, nil, "")
	return err
}
