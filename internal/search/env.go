package search

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Environment variables selecting and configuring the search backend
// (realtime reads them; docs/environment-variables.md).
const (
	EnvOpenSearchURL         = "BNBLOC_SEARCH_OPENSEARCH_URL"
	EnvOpenSearchUsername    = "BNBLOC_SEARCH_OPENSEARCH_USERNAME"
	EnvOpenSearchPassword    = "BNBLOC_SEARCH_OPENSEARCH_PASSWORD"
	EnvOpenSearchIndexPrefix = "BNBLOC_SEARCH_OPENSEARCH_INDEX_PREFIX"
)

// startupProbeBudget is how long a production start keeps probing the
// cluster before refusing to start (the cluster may be starting too). A
// variable only so tests can shorten it.
var startupProbeBudget = time.Minute

// FromEnv selects the search backend. Without BNBLOC_SEARCH_OPENSEARCH_URL
// it is the store index and no updater. With it, it is OpenSearch over
// chat's rows plus the updater that keeps it current (the caller runs it),
// after a startup probe (EnsureIndex): in production an unreachable cluster,
// refused credentials, or an index that is neither ready nor creatable is
// an error (the service refuses to start); in development it is a warning
// and every index call retries the probe. A malformed setting is an error
// in both modes.
func FromEnv(ctx context.Context, chat store.ChatIndex, metrics *obs.Metrics) (Index, *Updater, error) {
	raw := config.FromEnvRaw(EnvOpenSearchURL)
	if raw == "" {
		return NewStore(chat), nil, nil
	}
	idx, err := NewOpenSearch(OpenSearchConfig{
		URL:         raw,
		Username:    config.FromEnvRaw(EnvOpenSearchUsername),
		Password:    config.FromEnvRaw(EnvOpenSearchPassword),
		IndexPrefix: config.FromEnvRaw(EnvOpenSearchIndexPrefix),
	}, chat, metrics)
	if err != nil {
		return nil, nil, err
	}
	if err := probe(ctx, idx); err != nil {
		if config.Production() {
			return nil, nil, fmt.Errorf("%s is set but the search index is not usable: %w", EnvOpenSearchURL, err)
		}
		slog.Warn("bnbloc search: OpenSearch is not usable yet; searches and index updates fail until it is",
			"event", "bnbloc.search.probe_failed", "index", idx.IndexName(), "error", err)
	} else {
		slog.Info("bnbloc search: OpenSearch index ready", "event", "bnbloc.search.ready", "index", idx.IndexName())
	}
	return idx, NewUpdater(idx, metrics), nil
}

// probe runs EnsureIndex once in development and retries it for
// startupProbeBudget in production.
func probe(ctx context.Context, idx *OpenSearch) error {
	if !config.Production() {
		return idx.EnsureIndex(ctx)
	}
	deadline := time.Now().Add(startupProbeBudget)
	for wait := time.Second; ; wait *= 2 {
		err := idx.EnsureIndex(ctx)
		if err == nil || time.Now().Add(wait).After(deadline) {
			return err
		}
		slog.Warn("bnbloc search: OpenSearch probe failed; retrying", "event", "bnbloc.search.probe_failed", "error", err, "retry_in", wait.String())
		if !sleepCtx(ctx, wait) {
			return err
		}
	}
}
