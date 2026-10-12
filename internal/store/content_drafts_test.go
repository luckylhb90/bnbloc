package store

import (
	"context"
	"encoding/json"
	"errors"
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
	liveKey, goneKey := uniqBlobKey(), uniqBlobKey()
	draft := func(id, key string, expires int64) ContentDraft {
		return ContentDraft{ID: prefix + id, TenantID: prefix + "-t", RoomID: "room-1", JobID: "job-1", Requester: "maya", Author: "jack",
			Kind: "bnbloc.expert.package/v1", Name: "reviewer", DisplayName: "Reviewer", Version: "1.0.0",
			ManifestJSON:  json.RawMessage(`{"kind":"bnbloc.expert.package/v1","name":"reviewer","version":"1.0.0"}`),
			BundleBlobKey: key, SHA256: strings.TrimPrefix(key, "sha256/"), SizeBytes: 42, CreatedAtMs: now, ExpiresAtMs: expires}
	}
	live, gone := draft("-live", liveKey, now+ContentDraftTTL.Milliseconds()), draft("-gone", goneKey, now) // expired at now exactly
	for _, d := range []ContentDraft{live, gone} {
		if err := b.ContentComponents.PutContentDraft(ctx, d); err != nil {
			t.Fatalf("put draft: %v", err)
		}
	}
	again := live
	again.Author = "mallory"
	if err := b.ContentComponents.PutContentDraft(ctx, again); !errors.Is(err, ErrContentDraftExists) {
		t.Fatalf("second draft under one id = %v, want ErrContentDraftExists", err)
	}
	got, err := b.ContentComponents.GetContentDraft(ctx, live.ID)
	if err != nil || got.Requester != "maya" || got.Author != "jack" || got.JobID != "job-1" || got.BundleBlobKey != liveKey || !got.Live(now) || got.SizeBytes != 42 ||
		!got.Live(got.ExpiresAtMs-1) || got.Live(got.ExpiresAtMs) {
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

// Drafts on both backends: staged whole, live strictly before expiry, a
// taken id refused, kept referenced until the retention pass deletes the
// expired ones (at nowMs == expires included).
func TestContractContentComponentStoreDrafts(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) { exerciseContentDrafts(t, b, uniq("cd_")) })
}
