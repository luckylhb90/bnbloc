package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
)

// uniqBlobKey returns a content-addressed blob key no other test uses.
func uniqBlobKey() string {
	sum := sha256.Sum256([]byte(uniq("blob-")))
	return "sha256/" + hex.EncodeToString(sum[:])
}

var blobKeyColumnRe = regexp.MustCompile(`(?m)^\s*(\w*blob_key\w*)\s`)

// TestBlobKeyColumnsReferenced: every column of schema.sql that stores a
// blob key is in blobKeySources, the one list both ReferencedBlobKeys
// implementations read, and every listed source names a real column. A
// column missing here would let blob collection delete a blob a row still
// uses once the grace period passes.
func TestBlobKeyColumnsReferenced(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, src := range blobKeySources {
		listed[src.table+"."+src.column] = true
	}
	inSchema := map[string]bool{}
	for _, m := range createTableRe.FindAllStringSubmatch(string(schema), -1) {
		for _, c := range blobKeyColumnRe.FindAllStringSubmatch(m[2], -1) {
			col := m[1] + "." + c[1]
			inSchema[col] = true
			if !listed[col] {
				t.Errorf("%s stores a blob key but is not in blobKeySources (retention.go): blob collection would delete blobs it references", col)
			}
		}
	}
	if len(inSchema) < 5 {
		t.Fatalf("found only %d blob key columns in schema.sql; the parser is broken", len(inSchema))
	}
	for col := range listed {
		if !inSchema[col] {
			t.Errorf("blobKeySources lists %s, which schema.sql does not have", col)
		}
	}
}

// Both backends report a key as referenced from every source of
// blobKeySources (and from a platform setting), and never an unused key.
func TestContractRetentionStoreReferencedBlobKeys(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-blob-"))
		room := uniq("room-")
		art, err := b.Artifacts.PutArtifact(ctx, Artifact{ArtifactID: uniq("a-"), RoomID: room, Kind: "file", Title: "f", CreatedBy: "maya", CreatedAtMs: 1, UpdatedAtMs: 1})
		if err != nil {
			t.Fatal(err)
		}
		cc := ContentComponent{ID: uniq("cc-blob-"), Kind: "bnbloc.expert.package/v1", Name: uniq("n-"), TenantID: "t", Owner: "maya", Scope: ContentScopePersonal, Status: ContentStatusActive}
		// One seeder per source: a new source without a seeder fails below.
		seed := map[string]func(key string) error{
			"attachments.blob_key": func(key string) error {
				return b.Attachments.PutAttachment(ctx, Attachment{ID: uniq("att-"), RoomID: room, Uploader: "maya", Filename: "f", ContentType: "text/plain", BlobKey: key, CreatedAtMs: 1})
			},
			"artifact_versions.blob_key": func(key string) error {
				_, _, err := b.Artifacts.PublishArtifactVersion(ctx, art.ArtifactID, ArtifactVersion{Storage: ArtifactStorageBlob, BlobKey: key, ContentHash: strings.TrimPrefix(key, "sha256/"), SizeBytes: 1, MimeType: "application/pdf", AuthorUserID: "maya"}, 0)
				return err
			},
			"artifact_versions.thumb_blob_key": func(key string) error {
				_, _, err := b.Artifacts.SetArtifactThumbnail(ctx, art.ArtifactID, 1, ArtifactThumbnailWrite{BlobKey: key, MimeType: "image/png", Width: 1, Height: 1, Rank: 1})
				return err
			},
			"content_component_versions.bundle_blob_key": func(key string) error {
				return b.ContentComponents.CreateContentComponent(context.Background(), cc, contentVersion(cc.ID, "1.0.0", key, 1))
			},
			"app_files.blob_key": func(key string) error {
				appRoom := newContractRoom(t, ctx, b, "maya")
				k, err := b.AppDocs.EnsureAppKey(ctx, AppKey{AppID: uniq("app-"), RoomID: appRoom, WrappedKey: "wk", CreatedAtMs: 1})
				if err != nil {
					return err
				}
				return b.AppDocs.InsertAppFile(ctx, AppFile{AppID: k.AppID, FileID: uniq("f-"), RoomID: appRoom, ByKey: "p", NameSealed: []byte("n"), AuthorSealed: []byte("a"), ContentType: "image/png", Size: 1, BlobKey: key, CreatedAtMs: 1}, "wk")
			},
			"content_drafts.bundle_blob_key": func(key string) error {
				return b.ContentComponents.PutContentDraft(context.Background(), ContentDraft{ID: uniq("cd_blob-"), TenantID: "t", RoomID: room, JobID: "j", Kind: "k", Name: "n", Version: "1.0.0",
					ManifestJSON: []byte(`{}`), BundleBlobKey: key, SHA256: strings.TrimPrefix(key, "sha256/"), CreatedAtMs: 1, ExpiresAtMs: 1 << 50})
			},
		}
		keys := map[string]string{}
		var all []string
		for _, src := range blobKeySources {
			col := src.table + "." + src.column
			fn, ok := seed[col]
			if !ok {
				t.Fatalf("no seeder for blob key source %s: add one to this test", col)
			}
			key := uniqBlobKey()
			if err := fn(key); err != nil {
				t.Fatalf("seed %s: %v", col, err)
			}
			keys[col] = key
			all = append(all, key)
		}
		setting, free := uniqBlobKey(), uniqBlobKey()
		if err := b.Settings.PutPlatformSetting(ctx, PlatformSetting{Key: uniq("ext.test."), Value: `{"url":"https://gw.example/v1/artifacts/` + setting + `"}`, UpdatedAtMs: 1, UpdatedBy: "maya"}); err != nil {
			t.Fatal(err)
		}
		got, err := NewRetention(b).ReferencedBlobKeys(ctx, append(all, setting, free))
		if err != nil {
			t.Fatal(err)
		}
		for col, key := range keys {
			if !got[key] {
				t.Errorf("a key in %s is not reported referenced", col)
			}
		}
		if !got[setting] {
			t.Error("a key a platform setting mentions is not reported referenced")
		}
		if got[free] || len(got) != len(all)+1 {
			t.Fatalf("referenced = %v; an unused key was reported", got)
		}
		if none, err := NewRetention(b).ReferencedBlobKeys(ctx, nil); err != nil || none == nil || len(none) != 0 {
			t.Fatalf("no keys = %#v, %v", none, err)
		}
	})
}
