package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// exerciseContentDrafts: a staged draft reads back whole, keeps its bundle
// referenced while it exists, and the retention pass deletes it once it
// expired (then its bundle is unreferenced), leaving live drafts alone.
func exerciseContentDrafts(t *testing.T, b *Backends, prefix string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	liveKey := "sha256/" + strings.Repeat("7", 63) + "1"
	goneKey := "sha256/" + strings.Repeat("7", 63) + "2"
	draft := func(id, key string, expires int64) ContentDraft {
		return ContentDraft{ID: prefix + id, TenantID: prefix + "-t", RoomID: "room-1", JobID: "job-1", Requester: "maya", Author: "jack",
			Kind: "bnbloc.expert.package/v1", Name: "reviewer", DisplayName: "Reviewer", Version: "1.0.0",
			ManifestJSON:  json.RawMessage(`{"kind":"bnbloc.expert.package/v1","name":"reviewer","version":"1.0.0"}`),
			BundleBlobKey: key, SHA256: strings.TrimPrefix(key, "sha256/"), SizeBytes: 42, CreatedAtMs: now, ExpiresAtMs: expires}
	}
	live, gone := draft("-live", liveKey, now+ContentDraftTTL.Milliseconds()), draft("-gone", goneKey, now-1)
	for _, d := range []ContentDraft{live, gone} {
		if err := b.ContentComponents.PutContentDraft(ctx, d); err != nil {
			t.Fatalf("put draft: %v", err)
		}
	}
	got, err := b.ContentComponents.GetContentDraft(ctx, live.ID)
	if err != nil || got.Requester != "maya" || got.Author != "jack" || got.JobID != "job-1" || got.BundleBlobKey != liveKey || !got.Live(now) || got.SizeBytes != 42 {
		t.Fatalf("draft = %+v, %v", got, err)
	}
	var m map[string]any
	if json.Unmarshal(got.ManifestJSON, &m) != nil || m["name"] != "reviewer" {
		t.Fatalf("manifest = %s", got.ManifestJSON)
	}
	r := NewRetention(b)
	refs, err := r.ReferencedBlobKeys(ctx, []string{liveKey, goneKey})
	if err != nil || !refs[liveKey] || !refs[goneKey] {
		t.Fatalf("refs before purge = %+v, %v", refs, err)
	}
	res, err := r.PurgeExpiredDrafts(ctx, PurgeRequest{NowMs: now, Batch: 1})
	if err != nil || res[PurgedContentDrafts] < 1 {
		t.Fatalf("purge = %+v, %v", res, err)
	}
	if _, err := b.ContentComponents.GetContentDraft(ctx, gone.ID); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("expired draft after purge: %v", err)
	}
	if _, err := b.ContentComponents.GetContentDraft(ctx, live.ID); err != nil {
		t.Fatalf("live draft purged: %v", err)
	}
	refs, err = r.ReferencedBlobKeys(ctx, []string{liveKey, goneKey})
	if err != nil || !refs[liveKey] || refs[goneKey] {
		t.Fatalf("refs after purge = %+v, %v; the expired draft's bundle is unreferenced", refs, err)
	}
	if _, err := b.ContentComponents.GetContentDraft(ctx, prefix+"-missing"); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("missing draft: %v", err)
	}
}

func TestMemoryContentDrafts(t *testing.T) {
	b, err := NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	exerciseContentDrafts(t, b, "cd_mem")
}

func TestMySQLContentDrafts(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	prefix := "cd_it" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM content_drafts WHERE id LIKE ?`, prefix+"%")
		_ = b.DB.Close()
	})
	exerciseContentDrafts(t, b, prefix)
}
