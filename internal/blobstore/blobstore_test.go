package blobstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDiskStore_PutGetDelete_roundtrips_and_dedupes_content(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDiskStore: %v", err)
	}
	content := []byte("hello attachment")

	// When
	key, size, sha256hex, err := store.Put(ctx, bytes.NewReader(content))

	// Then
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !regexp.MustCompile(`^sha256/[0-9a-f]{64}$`).MatchString(key) {
		t.Fatalf("key format = %q", key)
	}
	if len(sha256hex) != 64 || key != "sha256/"+sha256hex {
		t.Fatalf("key %q and hash %q mismatch", key, sha256hex)
	}
	if size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", size, len(content))
	}
	r, gotSize, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotSize != int64(len(content)) {
		t.Fatalf("Get size = %d, want %d", gotSize, len(content))
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content = %q, want %q", got, content)
	}

	// When
	secondKey, secondSize, secondHash, err := store.Put(ctx, bytes.NewReader(content))

	// Then
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if secondKey != key || secondHash != sha256hex || secondSize != size {
		t.Fatalf("dedupe = (%q,%d,%q), want (%q,%d,%q)", secondKey, secondSize, secondHash, key, size, sha256hex)
	}

	// When
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, _, err = store.Get(ctx, key)

	// Then
	if !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("Get after Delete error = %v, want %v", err, ErrBlobNotFound)
	}
}

func TestDiskStore_Get_rejects_invalid_key(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDiskStore: %v", err)
	}

	// When
	_, _, err = store.Get(ctx, "../etc/passwd")

	// Then
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Get error = %v, want %v", err, ErrInvalidKey)
	}
}

func TestContentKey_returns_sha256_key_and_hex(t *testing.T) {
	// Given
	content := []byte("hello attachment")

	// When
	key, sha256hex := ContentKey(content)

	// Then
	if key != "sha256/"+sha256hex {
		t.Fatalf("key %q and hash %q mismatch", key, sha256hex)
	}
	if !regexp.MustCompile(`^sha256/[0-9a-f]{64}$`).MatchString(key) {
		t.Fatalf("key format = %q", key)
	}
}

func TestS3Store_normalizes_key_prefix_and_session_token(t *testing.T) {
	store, err := NewS3Store(S3Config{
		Bucket:          "bucket",
		Region:          "ap-northeast-1",
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
		SessionToken:    "token",
		KeyPrefix:       "/bnbloc/",
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	if store.cfg.KeyPrefix != "bnbloc/" {
		t.Fatalf("KeyPrefix = %q, want bnbloc/", store.cfg.KeyPrefix)
	}
	creds, err := store.provider.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if creds.SessionToken != "token" {
		t.Fatalf("SessionToken = %q, want token", creds.SessionToken)
	}
}

func TestS3Store_Put_doesNotDoubleCloseTempFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	store, err := NewS3Store(S3Config{
		Endpoint:        server.URL,
		Bucket:          "bucket",
		Region:          "ap-northeast-1",
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	if _, _, _, err := store.Put(context.Background(), bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// The S3 client must not bound the whole exchange: http.Client.Timeout also
// covers reading the body, which cut every blob stream longer than the limit
// (2026-10-09: the 249 MiB guest image never downloaded). Only the quick
// phases are bounded; a slow body streams to completion.
func TestS3Store_Get_streamsSlowBodyWithoutOverallTimeout(t *testing.T) {
	const chunks = 5
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(chunks*1024))
		w.WriteHeader(http.StatusOK)
		for i := 0; i < chunks; i++ {
			_, _ = w.Write(bytes.Repeat([]byte{'x'}, 1024))
			w.(http.Flusher).Flush()
			time.Sleep(200 * time.Millisecond)
		}
	}))
	t.Cleanup(server.Close)
	store, err := NewS3Store(S3Config{Endpoint: server.URL, Bucket: "bucket", Region: "ap-northeast-1", AccessKeyID: "ak", SecretAccessKey: "sk", UsePathStyle: true})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	if store.client.Timeout != 0 {
		t.Fatalf("S3 client has an overall timeout (%s): it would cut long body streams", store.client.Timeout)
	}
	tr, ok := store.client.Transport.(*http.Transport)
	if !ok || tr.ResponseHeaderTimeout <= 0 || tr.TLSHandshakeTimeout <= 0 {
		t.Fatalf("S3 transport must bound the header/handshake phases: %+v", store.client.Transport)
	}
	rc, size, err := store.Get(context.Background(), "sha256/"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	n, err := io.Copy(io.Discard, rc)
	if err != nil || n != chunks*1024 || size != chunks*1024 {
		t.Fatalf("read %d of %d (size %d): %v", n, chunks*1024, size, err)
	}
}
