package realtimehttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/realtime"
)

const presignKey = "sha256/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func presignServer(t *testing.T, blobs BlobStore, direct bool) *realtime.HandoffClient {
	t.Helper()
	svc := &Service{Hub: realtime.NewHub(4, 4), Blobs: blobs, DirectDownloads: direct}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return realtime.NewHandoffClient(srv.URL, "gateway")
}

func newPresignS3(t *testing.T) *blobstore.S3Store {
	t.Helper()
	s3, err := blobstore.NewS3Store(blobstore.S3Config{
		Bucket: "bucket", Region: "us-east-1", AccessKeyID: "AKID", SecretAccessKey: "SECRET",
		Endpoint: "http://minio:9000", PublicEndpoint: "https://files.example.com", UsePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s3
}

func TestBlobPresign_returns_a_public_presigned_url_when_enabled(t *testing.T) {
	client := presignServer(t, newPresignS3(t), true)
	before := time.Now()

	got, err := client.PresignBlob(context.Background(), presignKey, "Q3 report.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(got.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "files.example.com" || u.Path != "/bucket/"+presignKey {
		t.Fatalf("url = %s", got.URL)
	}
	q := u.Query()
	if q.Get("response-content-type") != "application/pdf" || q.Get("response-content-disposition") != `attachment; filename="Q3 report.pdf"` {
		t.Fatalf("overrides = %v", q)
	}
	if q.Get("X-Amz-Expires") != "300" {
		t.Fatalf("ttl = %s", q.Get("X-Amz-Expires"))
	}
	if exp := time.UnixMilli(got.ExpiresAtMs); exp.Before(before.Add(4*time.Minute)) || exp.After(time.Now().Add(6*time.Minute)) {
		t.Fatalf("expiresAtMs = %v", exp)
	}
}

func TestBlobPresign_is_404_disabled_when_switched_off_or_not_s3(t *testing.T) {
	disk, err := blobstore.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]*realtime.HandoffClient{
		"switch off": presignServer(t, newPresignS3(t), false),
		"disk store": presignServer(t, disk, true),
		"no store":   presignServer(t, nil, true),
	} {
		_, err := client.PresignBlob(context.Background(), presignKey, "", "")
		if !errors.Is(err, realtime.ErrDirectDownloadsDisabled) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestBlobPresign_refuses_bad_input_and_unsigned_callers(t *testing.T) {
	client := presignServer(t, newPresignS3(t), true)
	_, err := client.PresignBlob(context.Background(), "../etc/passwd", "", "")
	var se *realtime.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		t.Fatalf("bad key err = %v", err)
	}
	_, err = client.PresignBlob(context.Background(), presignKey, "", "not a type;;")
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		t.Fatalf("bad type err = %v", err)
	}
	// No asserted identity: the internal guard refuses before the handler.
	resp, err := http.Post(client.BaseURL+"/internal/v1/blobs/presign", "application/json", strings.NewReader(`{"key":"`+presignKey+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", resp.StatusCode)
	}
}
