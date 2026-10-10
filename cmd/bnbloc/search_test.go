package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// `bnbloc search reindex` loops on the returned cursor until done, and names
// the cursor to resume from when a call fails.
func TestSearchReindexLoopsUntilDone(t *testing.T) {
	isolateHome(t)
	var cursors []string
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/platform/search/reindex" {
			http.NotFound(w, r)
			return
		}
		var req wire.SearchReindexRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		cursors = append(cursors, req.Cursor)
		if fail && req.Cursor == "r1/500" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		next := map[string]wire.SearchReindexResponse{
			"":       {Scanned: 500, Updated: 120, Cursor: "r1/500"},
			"r1/500": {Scanned: 30, Updated: 3, Cursor: "r2/9", Done: true},
		}[req.Cursor]
		_ = json.NewEncoder(w).Encode(next)
	}))
	defer srv.Close()
	c := newClient(resolveConfig(srv.URL, "sess"))

	var out bytes.Buffer
	if err := searchReindex(context.Background(), c, "", 0, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cursors, ",") != ",r1/500" {
		t.Fatalf("cursors sent = %q", cursors)
	}
	if !strings.Contains(out.String(), "done: scanned 530 messages, indexed 123") {
		t.Fatalf("output:\n%s", out.String())
	}

	fail, cursors = true, nil
	err := searchReindex(context.Background(), c, "", 0, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `resume with --cursor "r1/500"`) {
		t.Fatalf("err = %v, want a resume hint", err)
	}
}

// `bnbloc search status` prints the backend and fails when it is not ready.
func TestSearchStatus(t *testing.T) {
	isolateHome(t)
	docs := int64(42)
	status := wire.SearchStatus{Backend: "opensearch", Index: "bnbloc-room-messages-v1", Ready: true, Health: "yellow", Documents: &docs,
		Updates: &wire.SearchUpdateStats{Applied: 40, Dropped: 2}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/platform/search/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer srv.Close()
	c := newClient(resolveConfig(srv.URL, "sess"))
	var out bytes.Buffer
	if err := searchStatus(context.Background(), c, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"backend:   opensearch", "documents: 42", "dropped 2", "run `bnbloc search reindex`"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}
	status = wire.SearchStatus{Backend: "opensearch", Error: "connection refused"}
	if err := searchStatus(context.Background(), c, &bytes.Buffer{}); err == nil {
		t.Fatal("a not-ready backend exited zero")
	}
}
