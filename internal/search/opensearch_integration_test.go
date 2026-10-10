package search

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// Against a real cluster (BNBLOC_TEST_OPENSEARCH_URL, e.g.
// opensearchproject/opensearch:2 single-node with the security plugin
// disabled): concurrent index creation, the analyzer's real tokens, the
// substring semantics the MySQL path has (English inside words, CJK,
// case, accents, full-width forms), and the mapping guard.
func TestOpenSearchRealCluster(t *testing.T) {
	base := os.Getenv("BNBLOC_TEST_OPENSEARCH_URL")
	if base == "" {
		t.Skip("set BNBLOC_TEST_OPENSEARCH_URL to run the OpenSearch integration test")
	}
	prefix := "bnbloc-itest-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	chat := store.NewMemoryChat()
	open := func(prefix string) *OpenSearch {
		o, err := NewOpenSearch(OpenSearchConfig{URL: base, IndexPrefix: prefix}, chat, nil)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	cleanup := func(index string) {
		t.Cleanup(func() {
			req, _ := http.NewRequest(http.MethodDelete, base+"/"+index, nil)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		})
	}
	o := open(prefix)
	cleanup(o.IndexName())

	// Several replicas starting together all succeed; one creates.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = open(prefix).EnsureIndex(context.Background())
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
	}
	if err := o.EnsureIndex(context.Background()); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"analyzer": textAnalyzer, "text": "Deploy 部署窗口 Ｃａｆé x"})
	resp, err := http.Post(base+"/"+o.IndexName()+"/_analyze", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var analyzed struct {
		Tokens []struct {
			Token string `json:"token"`
		} `json:"tokens"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&analyzed)
	resp.Body.Close()
	var tokens []string
	for _, tk := range analyzed.Tokens {
		tokens = append(tokens, tk.Token)
	}
	if got := strings.Join(tokens, " "); got != "de ep pl lo oy 部署 署窗 窗口 ca af fe" {
		t.Fatalf("analyzed tokens = %q", got)
	}

	texts := []string{"The deploy window is Friday", "部署窗口在周五", "Ｄｅｐｌｏｙ at the café", "data pipeline is ready"}
	var keys []store.MessageKey
	for i, text := range texts {
		chatRow(t, chat, acme, "room-1", int64(i+1), "maya", text, int64(100*(i+1)))
		keys = append(keys, store.MessageKey{TenantID: acme, RoomID: "room-1", Seq: int64(i + 1)})
	}
	if err := o.Apply(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	if err := o.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		q    string
		want string
	}{
		{"DEPLOY", "3,1"},
		{"ploy", "3,1"},
		{"窗口", "2"},
		{"cafe", "3"},
		{"data", "4"},
		{"ow is", "1"},
		{"w i", ""},
	} {
		got, err := o.Search(acmeCtx(), store.MessageSearch{RoomIDs: []string{"room-1"}, Text: tc.q})
		if err != nil {
			t.Fatal(err)
		}
		var seqs []string
		for _, m := range got {
			seqs = append(seqs, strconv.FormatInt(m.Seq, 10))
		}
		if strings.Join(seqs, ",") != tc.want {
			t.Errorf("search %q = %v, want %q", tc.q, seqs, tc.want)
		}
	}
	st := o.Status(context.Background())
	if !st.Ready || st.Documents == nil || *st.Documents != 4 {
		t.Fatalf("status = %+v", st)
	}

	// An index created by something else (another mapping) is refused.
	bad := open(prefix + "-bad")
	cleanup(bad.IndexName())
	req, _ := http.NewRequest(http.MethodPut, base+"/"+bad.IndexName(), strings.NewReader(`{"mappings":{"properties":{"text":{"type":"text"}}}}`))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	if err := bad.EnsureIndex(context.Background()); err == nil || !strings.Contains(err.Error(), "bnbloc search reindex") {
		t.Fatalf("foreign mapping: err = %v", err)
	}
}
