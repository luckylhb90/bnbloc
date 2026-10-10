package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

type S3Store struct {
	cfg      S3Config
	client   *http.Client
	endpoint *url.URL
	// publicEndpoint is the base of presigned URLs (endpoint when no
	// public endpoint is configured).
	publicEndpoint *url.URL
	provider       aws.CredentialsProvider
	now            func() time.Time
}

func NewS3Store(cfg S3Config) (*S3Store, error) {
	if strings.TrimSpace(cfg.Region) == "" {
		cfg.Region = "us-east-1"
	}
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.AccessKeyID = strings.TrimSpace(cfg.AccessKeyID)
	cfg.SecretAccessKey = strings.TrimSpace(cfg.SecretAccessKey)
	cfg.SessionToken = strings.TrimSpace(cfg.SessionToken)
	cfg.KeyPrefix = normalizeS3KeyPrefix(cfg.KeyPrefix)
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("s3 blobstore bucket required")
	}
	provider := cfg.CredentialProvider
	if provider == nil && cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		provider = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: cfg.AccessKeyID, SecretAccessKey: cfg.SecretAccessKey, SessionToken: cfg.SessionToken, Source: "bnbloc config"}, nil
		})
	}
	if provider == nil {
		return nil, fmt.Errorf("s3 blobstore credentials required: access key or secret key is empty after resolving env/default credentials")
	}
	endpoint, err := parseS3Endpoint(cfg.Endpoint, cfg.Region)
	if err != nil {
		return nil, err
	}
	publicEndpoint := endpoint
	if cfg.PublicEndpoint = strings.TrimSpace(cfg.PublicEndpoint); cfg.PublicEndpoint != "" {
		if publicEndpoint, err = parseS3Endpoint(cfg.PublicEndpoint, cfg.Region); err != nil {
			return nil, fmt.Errorf("s3 public endpoint: %w", err)
		}
	}
	return &S3Store{
		cfg:            cfg,
		client:         newS3HTTPClient(),
		endpoint:       endpoint,
		publicEndpoint: publicEndpoint,
		provider:       provider,
		now:            time.Now,
	}, nil
}

// newS3HTTPClient bounds only the phases that must be quick (dial, TLS,
// waiting for response headers). It has no overall timeout: an
// http.Client.Timeout also covers reading the body, which cut every blob
// stream that took longer than the limit (large artifacts, slow clients).
// The caller's context cancels a transfer.
func newS3HTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}}
}

func normalizeS3KeyPrefix(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

func (s *S3Store) Put(ctx context.Context, r io.Reader) (key string, size int64, sha256hex string, err error) {
	tmp, err := os.CreateTemp("", "bnbloc-blobstore-s3-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create s3 blob temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		err = errors.Join(err, os.Remove(tmpName))
	}()

	h := sha256.New()
	size, err = io.Copy(tmp, io.TeeReader(&contextReader{ctx: ctx, r: r}, h))
	if err != nil {
		_ = tmp.Close()
		return "", 0, "", fmt.Errorf("write s3 blob temp file: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return "", 0, "", fmt.Errorf("rewind s3 blob temp file: %w", err)
	}
	sha256hex = hex.EncodeToString(h.Sum(nil))
	key = keyFromHash(sha256hex)
	if err := s.do(ctx, http.MethodPut, key, tmp, size, sha256hex); err != nil {
		_ = tmp.Close()
		return "", 0, "", err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, "", fmt.Errorf("close s3 blob temp file: %w", err)
	}
	return key, size, sha256hex, nil
}

func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if _, err := validateKey(key); err != nil {
		return nil, 0, err
	}
	req, err := s.newRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, 0, err
	}
	if err := s.sign(ctx, req, emptySHA256Hex); err != nil {
		return nil, 0, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("get s3 blob %s: %w", key, err)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("blob %s: %w", key, ErrBlobNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, 0, fmt.Errorf("get s3 blob %s: status %d: %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// ContentLength is -1 when S3 (or a compatible store) omits it.
	return resp.Body, resp.ContentLength, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	if _, err := validateKey(key); err != nil {
		return err
	}
	if err := s.do(ctx, http.MethodDelete, key, nil, 0, emptySHA256Hex); err != nil {
		return err
	}
	return nil
}

// Walk lists every blob under the store's key prefix with ListObjectsV2,
// one page (up to 1000 keys) at a time.
func (s *S3Store) Walk(ctx context.Context, fn func(Info) error) error {
	prefix := s.cfg.KeyPrefix + keyPrefix
	token := ""
	for {
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("prefix", prefix)
		if token != "" {
			q.Set("continuation-token", token)
		}
		u := s.bucketURL()
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return fmt.Errorf("create s3 list request: %w", err)
		}
		if err := s.sign(ctx, req, emptySHA256Hex); err != nil {
			return err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return fmt.Errorf("list s3 blobs: %w", err)
		}
		var page listBucketResult
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return fmt.Errorf("list s3 blobs: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decode s3 list: %w", err)
		}
		for _, obj := range page.Contents {
			key := strings.TrimPrefix(obj.Key, s.cfg.KeyPrefix)
			if _, err := validateKey(key); err != nil {
				continue
			}
			if err := fn(Info{Key: key, Size: obj.Size, ModTime: obj.LastModified}); err != nil {
				return err
			}
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return nil
		}
		token = page.NextContinuationToken
	}
}

// listBucketResult is the part of a ListObjectsV2 answer Walk reads.
type listBucketResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string    `xml:"Key"`
		Size         int64     `xml:"Size"`
		LastModified time.Time `xml:"LastModified"`
	} `xml:"Contents"`
}

// Stat reads a blob's size and Last-Modified with a HEAD request.
func (s *S3Store) Stat(ctx context.Context, key string) (Info, error) {
	if _, err := validateKey(key); err != nil {
		return Info{}, err
	}
	req, err := s.newRequest(ctx, http.MethodHead, key, nil)
	if err != nil {
		return Info{}, err
	}
	if err := s.sign(ctx, req, emptySHA256Hex); err != nil {
		return Info{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("head s3 blob %s: %w", key, err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Info{}, fmt.Errorf("blob %s: %w", key, ErrBlobNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Info{}, fmt.Errorf("head s3 blob %s: status %d", key, resp.StatusCode)
	}
	mod, err := http.ParseTime(resp.Header.Get("Last-Modified"))
	if err != nil {
		return Info{}, fmt.Errorf("head s3 blob %s: Last-Modified: %w", key, err)
	}
	return Info{Key: key, Size: resp.ContentLength, ModTime: mod}, nil
}

// bucketURL addresses the bucket itself (the ListObjectsV2 target),
// path-style or virtual-host like objectURL.
func (s *S3Store) bucketURL() *url.URL {
	u := *s.endpoint
	if s.cfg.UsePathStyle {
		u.Path = joinS3Path(s.cfg.Bucket)
	} else {
		u.Host = s.cfg.Bucket + "." + u.Host
		u.Path = "/"
	}
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return &u
}

func (s *S3Store) do(ctx context.Context, method, key string, body io.Reader, size int64, payloadHash string) error {
	req, err := s.newRequest(ctx, method, key, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	if err := s.sign(ctx, req, payloadHash); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s s3 blob %s: %w", method, key, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("drain s3 response: %w", err)
	}
	if method == http.MethodDelete && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s s3 blob %s: status %d: %s", method, key, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func (s *S3Store) sign(ctx context.Context, req *http.Request, payloadHash string) error {
	credential, err := s.credential(ctx)
	if err != nil {
		return err
	}
	return signV4(req, credential, payloadHash, s.now().UTC())
}

func (s *S3Store) credential(ctx context.Context) (sigV4Credential, error) {
	creds, err := s.provider.Retrieve(ctx)
	if err != nil {
		return sigV4Credential{}, fmt.Errorf("retrieve s3 credentials: %w", err)
	}
	return sigV4Credential{
		AccessKeyID:     strings.TrimSpace(creds.AccessKeyID),
		SecretAccessKey: strings.TrimSpace(creds.SecretAccessKey),
		SessionToken:    strings.TrimSpace(creds.SessionToken),
		Region:          s.cfg.Region,
		Service:         "s3",
	}, nil
}

// PresignGet returns a SigV4 query-signed GET URL for key, valid for ttl,
// addressed through the public endpoint with the store's path-style or
// virtual-host addressing. opts override the response's Content-Type and
// Content-Disposition (signed, so a client cannot change them).
func (s *S3Store) PresignGet(ctx context.Context, key string, ttl time.Duration, opts PresignOptions) (string, error) {
	if _, err := validateKey(key); err != nil {
		return "", err
	}
	credential, err := s.credential(ctx)
	if err != nil {
		return "", err
	}
	u := s.objectURL(s.publicEndpoint, key)
	q := url.Values{}
	if ct := strings.TrimSpace(opts.ContentType); ct != "" {
		q.Set("response-content-type", ct)
	}
	if cd := strings.TrimSpace(opts.ContentDisposition); cd != "" {
		q.Set("response-content-disposition", cd)
	}
	u.RawQuery = q.Encode()
	return presignV4(http.MethodGet, u, credential, s.now().UTC(), ttl)
}

// objectURL addresses key under base: path-style (/<bucket>/<key>) or
// virtual-host (<bucket>.<host>/<key>). The path is AWS-URI-encoded into
// RawPath, so the signed canonical path is exactly the one on the wire.
func (s *S3Store) objectURL(base *url.URL, key string) *url.URL {
	u := *base
	objectKey := s.cfg.KeyPrefix + key
	if s.cfg.UsePathStyle {
		u.Path = joinS3Path(s.cfg.Bucket, objectKey)
	} else {
		u.Host = s.cfg.Bucket + "." + u.Host
		u.Path = joinS3Path(objectKey)
	}
	u.RawPath = awsURIEncode(u.Path, false)
	u.RawQuery = ""
	u.Fragment = ""
	return &u
}

func (s *S3Store) newRequest(ctx context.Context, method, key string, body io.Reader) (*http.Request, error) {
	u := s.objectURL(s.endpoint, key)
	if body != nil {
		body = readerOnly{Reader: body}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create s3 request: %w", err)
	}
	return req, nil
}

type readerOnly struct {
	io.Reader
}

func parseS3Endpoint(raw string, region string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "https://s3." + region + ".amazonaws.com"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse s3 endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("s3 endpoint must be absolute http(s) URL")
	}
	return u, nil
}

func joinS3Path(parts ...string) string {
	return "/" + strings.Join(parts, "/")
}
