package search

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/search/searchtest"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// No URL selects the store index (no updater); a malformed setting is an
// error everywhere; an unusable cluster is a warning in development and a
// startup error in production.
func TestFromEnv(t *testing.T) {
	chat := store.NewMemoryChat()
	for _, unset := range []string{"", "  "} { // blank is unset, never fatal
		t.Setenv(EnvOpenSearchURL, unset)
		idx, updates, err := FromEnv(context.Background(), chat, nil)
		if err != nil || idx.Backend() != BackendMySQL || updates != nil {
			t.Fatalf("unset (%q) = %v %v %v", unset, idx, updates, err)
		}
	}

	t.Setenv(EnvOpenSearchURL, "https://search:9200")
	t.Setenv(EnvOpenSearchUsername, "svc-search")
	if _, _, err := FromEnv(context.Background(), chat, nil); err == nil || !strings.Contains(err.Error(), "both a username and a password") {
		t.Fatalf("half credentials = %v", err)
	}
	t.Setenv(EnvOpenSearchUsername, "")

	fake := searchtest.New(t)
	fake.FailWith(func(*http.Request) int { return http.StatusServiceUnavailable })
	t.Setenv(EnvOpenSearchURL, fake.URL)
	t.Setenv(EnvOpenSearchIndexPrefix, "acme-dev")
	t.Setenv("BNBLOC_ENV", "")
	idx, updates, err := FromEnv(context.Background(), chat, nil)
	if err != nil || idx.Backend() != BackendOpenSearch || updates == nil {
		t.Fatalf("development with the cluster down = %v %v %v, want a warning only", idx, updates, err)
	}
	if got := idx.(*OpenSearch).IndexName(); got != "acme-dev-room-messages-v1" {
		t.Fatalf("index = %q", got)
	}

	t.Setenv("BNBLOC_ENV", "production")
	defer func(d time.Duration) { startupProbeBudget = d }(startupProbeBudget)
	startupProbeBudget = 10 * time.Millisecond
	if _, _, err := FromEnv(context.Background(), chat, nil); err == nil || !strings.Contains(err.Error(), EnvOpenSearchURL) {
		t.Fatalf("production with the cluster down = %v, want a startup error", err)
	}
	fake.FailWith(nil)
	if idx, _, err := FromEnv(context.Background(), chat, nil); err != nil || !idx.Status(context.Background()).Ready {
		t.Fatalf("production with the cluster up = %v", err)
	}
}
