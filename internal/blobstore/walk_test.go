package blobstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Walk lists committed blobs only, Stat reports them, and storing content
// that already exists refreshes its modification time (the blob
// collection's grace period relies on it).
func TestDiskStore_Walk_Stat_and_rePut_refreshes_mtime(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewDiskStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _, err := store.Put(ctx, strings.NewReader("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := store.Put(ctx, strings.NewReader("bb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "put-123"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	var keys []string
	if err := store.Walk(ctx, func(i Info) error { keys = append(keys, i.Key); return nil }); err != nil {
		t.Fatal(err)
	}
	sort.Strings(keys)
	want := []string{a, b}
	sort.Strings(want)
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("walk = %v, want %v", keys, want)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(store.pathForHash(strings.TrimPrefix(a, "sha256/")), old, old); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(ctx, a)
	if err != nil || info.Size != 1 || info.ModTime.After(old.Add(time.Second)) {
		t.Fatalf("stat = %+v, %v", info, err)
	}
	if _, _, _, err := store.Put(ctx, bytes.NewReader([]byte("a"))); err != nil {
		t.Fatal(err)
	}
	if info, _ = store.Stat(ctx, a); time.Since(info.ModTime) > time.Hour {
		t.Fatalf("re-put must refresh the mtime: %v", info.ModTime)
	}
	if err := store.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, a); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("stat of a deleted blob = %v", err)
	}
	stop := errors.New("stop")
	if err := store.Walk(ctx, func(Info) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("walk must return fn's error: %v", err)
	}
}

// Walk pages through ListObjectsV2 under the key prefix (continuation
// tokens, prefix stripped, foreign keys skipped); Stat reads HEAD.
func TestS3Store_Walk_pages_and_Stat_heads(t *testing.T) {
	k1 := "sha256/" + strings.Repeat("a", 64)
	k2 := "sha256/" + strings.Repeat("b", 64)
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Errorf("unsigned request %s %s", r.Method, r.URL)
		}
		if r.Method == http.MethodHead {
			if r.URL.Path != "/bucket/tenantdata/"+k1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Last-Modified", "Wed, 01 Oct 2026 10:00:00 GMT")
			w.Header().Set("Content-Length", "7")
			return
		}
		if r.URL.Path != "/bucket" || r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("prefix") != "tenantdata/sha256/" {
			t.Errorf("list request = %s", r.URL)
		}
		queries = append(queries, r.URL.Query().Get("continuation-token"))
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("continuation-token") == "" {
			fmt.Fprintf(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>tok+/=</NextContinuationToken>
<Contents><Key>tenantdata/%s</Key><Size>7</Size><LastModified>2026-10-01T10:00:00.000Z</LastModified></Contents>
<Contents><Key>tenantdata/sha256/not-a-key</Key><Size>1</Size><LastModified>2026-10-01T10:00:00.000Z</LastModified></Contents>
</ListBucketResult>`, k1)
			return
		}
		fmt.Fprintf(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
<Contents><Key>tenantdata/%s</Key><Size>9</Size><LastModified>2026-10-02T10:00:00.000Z</LastModified></Contents>
</ListBucketResult>`, k2)
	}))
	defer server.Close()
	store, err := NewS3Store(S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "bucket", AccessKeyID: "AK", SecretAccessKey: "SK", KeyPrefix: "tenantdata", UsePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var got []Info
	if err := store.Walk(ctx, func(i Info) error { got = append(got, i); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != k1 || got[1].Key != k2 || got[1].Size != 9 || !got[0].ModTime.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("walk = %+v", got)
	}
	if fmt.Sprint(queries) != "[ tok+/=]" {
		t.Fatalf("continuation tokens = %q", queries)
	}
	info, err := store.Stat(ctx, k1)
	if err != nil || info.Size != 7 || !info.ModTime.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("stat = %+v, %v", info, err)
	}
	if _, err := store.Stat(ctx, k2); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("stat of a missing blob = %v", err)
	}
}
