package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// HandoffClient is a service's client onto realtime: the room service's side
// of a room handoff package (reading the whole feed, reading/writing
// attachment blobs through realtime's own type-gated, size-capped attachment
// routes, the history-only import write), and the content-addressed blob
// store (PutBlob / OpenBlob / PresignBlob) that backs blob artifact versions,
// content bundles and distributed artifacts. A handoff call forwards the END
// USER's identity, so realtime applies the same membership checks it applies
// to that user's own requests; a blob call asserts the calling service (the
// blob routes are service-only: the caller has already authorized the read).
// Every call carries ctx's tenant and is svcauth-signed.
type HandoffClient struct {
	BaseURL string
	// Service names the calling service, asserted on blob calls.
	Service string
	// Key signs the assertion; empty only in dev (svcauth dev mode).
	Key string
	// HTTP has no global timeout: an export streams a whole room. Callers
	// bound each call with their request context.
	HTTP *http.Client
}

// NewHandoffClient builds service's client for realtime at baseURL, signing
// with the configured internal key. An empty baseURL leaves the client
// unconfigured (every call fails with ErrHandoffUnconfigured).
func NewHandoffClient(baseURL, service string) *HandoffClient {
	return &HandoffClient{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Service: service,
		Key:     svcauth.KeyFromEnv(),
		HTTP:    &http.Client{},
	}
}

// ErrHandoffUnconfigured reports a client with no realtime base URL.
var ErrHandoffUnconfigured = fmt.Errorf("realtime handoff: no realtime base URL configured")

// StatusError is a non-2xx answer from realtime; Status lets callers tell a
// refusal of the input (4xx) from a backend failure (5xx).
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("realtime handoff: status %d: %s", e.Status, strings.TrimSpace(e.Body))
}

func (c *HandoffClient) do(ctx context.Context, method, path, user string, body io.Reader, contentType string) (*http.Response, error) {
	if c == nil || c.BaseURL == "" {
		return nil, ErrHandoffUnconfigured
	}
	req, err := svcauth.Key(c.Key).NewRequest(ctx, method, c.BaseURL+path, body, user, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, &StatusError{Status: resp.StatusCode, Body: httpx.ErrorText(b)}
	}
	return resp, nil
}

func roomPath(prefix, roomID, suffix string) string {
	return prefix + "/rooms/" + url.PathEscape(roomID) + suffix
}

// Feed returns one ascending page of the room's persisted feed after a
// sequence cursor (every channel, soft-deleted rows excluded).
func (c *HandoffClient) Feed(ctx context.Context, user, roomID string, after int64, limit int) ([]wire.FeedMessage, error) {
	q := url.Values{}
	q.Set("after", strconv.FormatInt(after, 10))
	q.Set("limit", strconv.Itoa(limit))
	resp, err := c.do(ctx, http.MethodGet, roomPath("/internal/v1", roomID, "/feed?"+q.Encode()), user, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var page wire.FeedPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("realtime handoff: decode feed: %w", err)
	}
	return page.Messages, nil
}

// OpenAttachment streams one attachment's bytes (the member download route).
func (c *HandoffClient) OpenAttachment(ctx context.Context, user, roomID, attachmentID string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, roomPath("/v1", roomID, "/attachments/"+url.PathEscape(attachmentID)), user, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// UploadAttachment re-uploads attachment bytes into a room through the member
// upload route (declared-type allowlist, content sniffing, size cap, and
// content-addressed blob storage all apply) and returns the new attachment id.
func (c *HandoffClient) UploadAttachment(ctx context.Context, user, roomID, filename, contentType string, content io.Reader) (string, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
		h.Set("Content-Type", contentType)
		part, err := mw.CreatePart(h)
		if err == nil {
			_, err = io.Copy(part, content)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	resp, err := c.do(ctx, http.MethodPost, roomPath("/v1", roomID, "/attachments"), user, pr, mw.FormDataContentType())
	// Unblock the writer goroutine whatever the outcome.
	_ = pr.CloseWithError(io.ErrClosedPipe)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var meta struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil || meta.ID == "" {
		return "", fmt.Errorf("realtime handoff: decode attachment upload: %v", err)
	}
	return meta.ID, nil
}

// ImportMessages persists one batch of imported history into the room (never
// published live).
func (c *HandoffClient) ImportMessages(ctx context.Context, user, roomID string, batch wire.RoomImportBatch) (wire.RoomImportBatchReply, error) {
	buf, err := json.Marshal(batch)
	if err != nil {
		return wire.RoomImportBatchReply{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, roomPath("/internal/v1", roomID, "/import"), user, bytes.NewReader(buf), "application/json")
	if err != nil {
		return wire.RoomImportBatchReply{}, err
	}
	defer resp.Body.Close()
	var reply wire.RoomImportBatchReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return wire.RoomImportBatchReply{}, fmt.Errorf("realtime handoff: decode import reply: %w", err)
	}
	return reply, nil
}

// BlobRef is a stored blob: its content-addressed key, sha256 and size.
type BlobRef struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// PutBlob streams content into realtime's content-addressed blob store (the
// single owner of blob storage) and returns its reference. Realtime caps one
// blob at 256 MiB. The same bytes always yield the same key.
func (c *HandoffClient) PutBlob(ctx context.Context, content io.Reader) (BlobRef, error) {
	resp, err := c.do(ctx, http.MethodPost, "/internal/v1/blobs", c.serviceSubject(), content, "application/octet-stream")
	if err != nil {
		return BlobRef{}, err
	}
	defer resp.Body.Close()
	var ref BlobRef
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil || ref.Key == "" {
		return BlobRef{}, fmt.Errorf("realtime blob put: decode reply: %v", err)
	}
	return ref, nil
}

// OpenBlob streams a stored blob by key and reports its size (-1 when
// unknown). The caller closes the reader.
func (c *HandoffClient) OpenBlob(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	resp, err := c.do(ctx, http.MethodGet, "/internal/v1/blobs/"+key, c.serviceSubject(), nil, "")
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

// BlobPresign is a short-lived direct download URL of a stored blob.
type BlobPresign struct {
	URL         string `json:"url"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// ErrDirectDownloadsDisabled reports that realtime hands out no presigned
// URLs (disk store, or BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS off): the
// caller streams the bytes itself.
var ErrDirectDownloadsDisabled = errors.New("realtime blob presign: direct downloads disabled")

// PresignBlob asks realtime for a presigned download URL of key whose
// response carries contentType and an attachment disposition naming
// fileName (both optional). The caller must already have authorized the
// read. ErrDirectDownloadsDisabled means: stream instead.
func (c *HandoffClient) PresignBlob(ctx context.Context, key, fileName, contentType string) (BlobPresign, error) {
	buf, err := json.Marshal(map[string]string{"key": key, "fileName": fileName, "contentType": contentType})
	if err != nil {
		return BlobPresign{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/internal/v1/blobs/presign", c.serviceSubject(), bytes.NewReader(buf), "application/json")
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return BlobPresign{}, ErrDirectDownloadsDisabled
	}
	if err != nil {
		return BlobPresign{}, err
	}
	defer resp.Body.Close()
	var out BlobPresign
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.URL == "" {
		return BlobPresign{}, fmt.Errorf("realtime blob presign: decode reply: %v", err)
	}
	return out, nil
}

func (c *HandoffClient) serviceSubject() string { return svcauth.ServiceSubject(c.Service) }
