// Package blobstore stores content-addressed chat attachment binaries.
package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/digest"
)

const (
	keyPrefix      = digest.KeyPrefix
	defaultDiskDir = "./.bnbloc-attachments"
)

var (
	ErrInvalidKey   = errors.New("blobstore: invalid key")
	ErrBlobNotFound = errors.New("blobstore: blob not found")
)

// BlobStore stores opaque binary blobs addressed by a content key.
type BlobStore interface {
	// Put streams content in, returns the content-addressed key ("sha256/<hex>")
	// and the number of bytes written. Idempotent: identical content -> same key.
	Put(ctx context.Context, r io.Reader) (key string, size int64, sha256hex string, err error)
	// Get opens the blob for reading and reports its size in bytes (-1 when
	// the backend cannot tell), so readers can send Content-Length and a cut
	// transfer is detectable. Caller closes.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	// Delete removes a blob (best-effort; missing is not an error).
	Delete(ctx context.Context, key string) error
	// Walk calls fn once for every stored blob, in no particular order; an
	// error from fn stops the walk and is returned. The blob collection
	// (internal/retention) walks the store to find unreferenced blobs.
	Walk(ctx context.Context, fn func(Info) error) error
	// Stat returns a blob's size and last write time, ErrBlobNotFound when
	// it is missing. A Put of content that is already stored rewrites it,
	// so ModTime is the last time anyone stored those bytes.
	Stat(ctx context.Context, key string) (Info, error)
}

// Info describes one stored blob.
type Info struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// Presigner is the optional direct-download capability of a BlobStore: a
// short-lived URL a client fetches the blob from without the bytes passing
// through bnbloc. Only S3Store implements it (a disk store has no URL a
// client could reach).
type Presigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration, opts PresignOptions) (string, error)
}

// PresignOptions override response headers of a presigned download (S3
// response-content-type / response-content-disposition). Empty = the
// object's stored headers.
type PresignOptions struct {
	ContentType        string
	ContentDisposition string
}

type S3Config struct {
	Endpoint string
	// PublicEndpoint, when set, is the base URL presigned URLs are issued
	// for: the S3 endpoint as clients reach it, when Endpoint is an internal
	// name only bnbloc resolves. Empty = Endpoint.
	PublicEndpoint     string
	Region             string
	Bucket             string
	AccessKeyID        string
	SecretAccessKey    string
	SessionToken       string
	CredentialProvider aws.CredentialsProvider
	KeyPrefix          string
	UsePathStyle       bool
}

func ContentKey(content []byte) (key string, sha256hex string) {
	sha256hex = digest.Of(content)
	return digest.Key(sha256hex), sha256hex
}

func FromEnv() (BlobStore, error) {
	if bucket := strings.TrimSpace(config.FromEnvRaw("BNBLOC_ATTACHMENTS_S3_BUCKET")); bucket != "" {
		usePathStyle, err := config.StrictBool("BNBLOC_ATTACHMENTS_S3_PATH_STYLE")
		if err != nil {
			return nil, err
		}
		region := firstEnv("BNBLOC_ATTACHMENTS_S3_REGION", "AWS_REGION", "AWS_DEFAULT_REGION")
		provider, err := s3CredentialProviderFromEnvOrAWS(context.Background(), region)
		if err != nil {
			return nil, err
		}
		return NewS3Store(S3Config{
			Endpoint:           strings.TrimSpace(config.FromEnvRaw("BNBLOC_ATTACHMENTS_S3_ENDPOINT")),
			PublicEndpoint:     strings.TrimSpace(config.FromEnvRaw("BNBLOC_ATTACHMENTS_S3_PUBLIC_ENDPOINT")),
			Region:             region,
			Bucket:             bucket,
			CredentialProvider: provider,
			KeyPrefix:          config.FromEnvRaw("BNBLOC_ATTACHMENTS_S3_KEY_PREFIX"),
			UsePathStyle:       usePathStyle,
		})
	}
	dir := strings.TrimSpace(config.FromEnvRaw("BNBLOC_ATTACHMENTS_DIR"))
	if dir == "" {
		dir = defaultDiskDir
	}
	shared, err := config.StrictBool(SharedDiskEnv)
	if err != nil {
		return nil, err
	}
	d, err := NewDiskStore(dir)
	if err != nil {
		return nil, err
	}
	d.shared = shared
	return d, nil
}

// DirectDownloadsFromEnv reads BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS (unset =
// false): whether downloads may be answered with a presigned URL. Only a
// store that implements Presigner can honour it.
func DirectDownloadsFromEnv() (bool, error) {
	return config.StrictBool("BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS")
}

func s3CredentialProviderFromEnvOrAWS(ctx context.Context, region string) (aws.CredentialsProvider, error) {
	accessKeyID := firstEnv("BNBLOC_ATTACHMENTS_S3_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID")
	secretAccessKey := firstEnv("BNBLOC_ATTACHMENTS_S3_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY")
	sessionToken := firstEnv("BNBLOC_ATTACHMENTS_S3_SESSION_TOKEN", "AWS_SESSION_TOKEN")
	if accessKeyID != "" && secretAccessKey != "" {
		return aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: accessKeyID, SecretAccessKey: secretAccessKey, SessionToken: sessionToken, Source: "bnbloc env"}, nil
		})), nil
	}
	if accessKeyID != "" || secretAccessKey != "" {
		return nil, fmt.Errorf("partial s3 blobstore credentials: both access key and secret key are required when using explicit credentials (set BNBLOC_ATTACHMENTS_S3_ACCESS_KEY_ID + BNBLOC_ATTACHMENTS_S3_SECRET_ACCESS_KEY, or AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY)")
	}
	awsCfg, loadErr := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if loadErr != nil {
		return nil, fmt.Errorf("s3 blobstore credentials required (set BNBLOC_ATTACHMENTS_S3_ACCESS_KEY_ID/SECRET_ACCESS_KEY, AWS_ACCESS_KEY_ID/SECRET_ACCESS_KEY, or configure the AWS default credential chain): %w", loadErr)
	}
	creds, retrieveErr := awsCfg.Credentials.Retrieve(ctx)
	if retrieveErr != nil {
		return nil, fmt.Errorf("s3 blobstore credentials required (set BNBLOC_ATTACHMENTS_S3_ACCESS_KEY_ID/SECRET_ACCESS_KEY, AWS_ACCESS_KEY_ID/SECRET_ACCESS_KEY, or configure the AWS default credential chain): %w", retrieveErr)
	}
	if strings.TrimSpace(creds.AccessKeyID) == "" || strings.TrimSpace(creds.SecretAccessKey) == "" {
		return nil, fmt.Errorf("s3 blobstore credentials required: AWS default credential chain returned incomplete credentials")
	}
	return awsCfg.Credentials, nil
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(config.FromEnvRaw(key)); v != "" {
			return v
		}
	}
	return ""
}

func validateKey(key string) (string, error) {
	hexPart, ok := digest.ParseKey(key)
	if !ok {
		return "", fmt.Errorf("key %q: %w", key, ErrInvalidKey)
	}
	return hexPart, nil
}
