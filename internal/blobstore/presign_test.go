package blobstore

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The AWS SigV4 query-authentication example ("Authenticating Requests:
// Using Query Parameters", Amazon S3 API reference): GET test.txt from
// examplebucket in us-east-1 at 20130524T000000Z, valid 86400 s.
func TestPresignV4_matches_the_AWS_query_auth_example(t *testing.T) {
	u, _ := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

	got, err := presignV4("GET", u, sigV4Credential{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:          "us-east-1",
		Service:         "s3",
	}, now, 86400*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	want := "https://examplebucket.s3.amazonaws.com/test.txt" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got != want {
		t.Fatalf("presigned URL\n got %s\nwant %s", got, want)
	}
}

func TestPresignV4_rejects_out_of_range_expiry(t *testing.T) {
	u, _ := url.Parse("https://b.s3.amazonaws.com/k")
	for _, ttl := range []time.Duration{0, 8 * 24 * time.Hour} {
		if _, err := presignV4("GET", u, sigV4Credential{Region: "us-east-1", Service: "s3"}, time.Now(), ttl); err == nil {
			t.Fatalf("ttl %v accepted", ttl)
		}
	}
}

func TestAWSURIEncode_follows_sigv4_rules(t *testing.T) {
	if got := awsURIEncode("a b+c/d~e_f.g-h=é", true); got != "a%20b%2Bc%2Fd~e_f.g-h%3D%C3%A9" {
		t.Fatalf("encode = %q", got)
	}
	if got := awsURIEncode("/pre fix/sha256/ab", false); got != "/pre%20fix/sha256/ab" {
		t.Fatalf("path encode = %q", got)
	}
}

const presignTestKey = "sha256/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newPresignTestStore(t *testing.T, cfg S3Config) *S3Store {
	t.Helper()
	cfg.Bucket = "bucket"
	cfg.Region = "eu-west-1"
	cfg.AccessKeyID = "AKID"
	cfg.SecretAccessKey = "SECRET"
	s, err := NewS3Store(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }
	return s
}

func TestS3Store_PresignGet_addresses_the_public_endpoint(t *testing.T) {
	cases := []struct {
		name string
		cfg  S3Config
		want string // scheme://host/path
	}{
		{"aws virtual-host", S3Config{}, "https://bucket.s3.eu-west-1.amazonaws.com/sha256/"},
		{"path-style endpoint", S3Config{Endpoint: "http://minio:9000", UsePathStyle: true}, "http://minio:9000/bucket/sha256/"},
		{"path-style public endpoint", S3Config{Endpoint: "http://minio:9000", PublicEndpoint: "https://files.example.com", UsePathStyle: true}, "https://files.example.com/bucket/sha256/"},
		{"virtual-host public endpoint", S3Config{Endpoint: "http://s3.internal", PublicEndpoint: "https://s3.example.com"}, "https://bucket.s3.example.com/sha256/"},
		{"key prefix", S3Config{KeyPrefix: "/team a/"}, "https://bucket.s3.eu-west-1.amazonaws.com/team%20a/sha256/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newPresignTestStore(t, tc.cfg)
			raw, err := s.PresignGet(context.Background(), presignTestKey, 5*time.Minute, PresignOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(raw, tc.want) {
				t.Fatalf("url %s does not start with %s", raw, tc.want)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			if q.Get("X-Amz-Expires") != "300" || q.Get("X-Amz-SignedHeaders") != "host" ||
				q.Get("X-Amz-Credential") != "AKID/20261009/eu-west-1/s3/aws4_request" || len(q.Get("X-Amz-Signature")) != 64 {
				t.Fatalf("query = %v", q)
			}
			if q.Has("X-Amz-Security-Token") {
				t.Fatal("static credentials must not carry a security token")
			}
		})
	}
}

func TestS3Store_PresignGet_signs_session_token_and_response_overrides(t *testing.T) {
	s := newPresignTestStore(t, S3Config{SessionToken: "tok/en+1"})
	raw, err := s.PresignGet(context.Background(), presignTestKey, time.Minute, PresignOptions{
		ContentType:        "application/pdf",
		ContentDisposition: `attachment; filename="q3 report.pdf"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("X-Amz-Security-Token") != "tok/en+1" {
		t.Fatalf("security token = %q", q.Get("X-Amz-Security-Token"))
	}
	if q.Get("response-content-type") != "application/pdf" || q.Get("response-content-disposition") != `attachment; filename="q3 report.pdf"` {
		t.Fatalf("response overrides = %v", q)
	}
	// SigV4 encoding on the wire: spaces are %20, never "+".
	if strings.Contains(u.RawQuery, "+") || !strings.Contains(u.RawQuery, "q3%20report.pdf") {
		t.Fatalf("raw query not sigv4-encoded: %s", u.RawQuery)
	}
	// The signature covers the overrides: changing one changes it.
	other, _ := s.PresignGet(context.Background(), presignTestKey, time.Minute, PresignOptions{ContentType: "text/html"})
	ou, _ := url.Parse(other)
	if ou.Query().Get("X-Amz-Signature") == q.Get("X-Amz-Signature") {
		t.Fatal("signature does not cover response overrides")
	}
}

func TestS3Store_PresignGet_rejects_invalid_keys(t *testing.T) {
	s := newPresignTestStore(t, S3Config{})
	if _, err := s.PresignGet(context.Background(), "../x", time.Minute, PresignOptions{}); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestDiskStore_is_not_a_presigner(t *testing.T) {
	d, err := NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var store BlobStore = d
	if _, ok := store.(Presigner); ok {
		t.Fatal("disk store must not presign")
	}
}
