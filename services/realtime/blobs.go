package realtimehttp

import (
	"encoding/json"
	"errors"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/obs"
)

const maxBlobUploadBytes = 256 * 1024 * 1024

// blobPutResult is the response of a successful generic blob upload: the
// content-addressed key the caller stores, plus the size and sha256 the blob
// store computed while streaming.
type blobPutResult struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type blobCapability struct {
	Enabled bool   `json:"enabled"`
	Backend string `json:"backend"`
}

func (s *Service) handleInternalBlobCapability(w http.ResponseWriter, _ *http.Request) {
	if s.Blobs == nil {
		obs.ServerError(w, "blob store unavailable", nil)
		http.Error(w, s.blobUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(blobCapability{Enabled: true, Backend: "blobstore"})
}

// handleInternalBlobPut streams a raw request body into the blob store and
// returns its content-addressed key. This is the generic, room-less blob
// primitive the gateway proxies to for artifact distribution (sidecar core
// binaries) — deliberately distinct from the attachment upload, which is
// room-scoped, type-gated (no executables), and size-capped for chat.
//
// It carries no room/tenant/membership semantics: the only guard is
// svcauth.Middleware (in cmd/main), so exclusively a signed gateway call
// reaches it. The body is the raw bytes (not multipart) — the gateway forwards
// the artifact stream verbatim.
func (s *Service) handleInternalBlobPut(w http.ResponseWriter, r *http.Request) {
	if s.Blobs == nil {
		obs.ServerError(w, "blob store unavailable", nil)
		http.Error(w, s.blobUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBlobUploadBytes)
	key, size, sha, err := s.Blobs.Put(r.Context(), r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "blob too large", http.StatusRequestEntityTooLarge)
			return
		}
		obs.ServerError(w, "blob store failed", err)
		http.Error(w, "blob store failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(blobPutResult{Key: key, SHA256: sha, Size: size})
}

// handleInternalBlobGet streams a stored blob back by its content-addressed
// key. The key is the full "sha256/<hex>" path captured by the {key...}
// wildcard. Mirrors the attachment download's stream shape (nosniff), minus the
// room-membership gate: same svcauth-only trust as the put side.
func (s *Service) handleInternalBlobGet(w http.ResponseWriter, r *http.Request) {
	if s.Blobs == nil {
		obs.ServerError(w, "blob store unavailable", nil)
		http.Error(w, s.blobUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}
	key := strings.TrimPrefix(r.PathValue("key"), "/")
	rc, size, err := s.Blobs.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, blobstore.ErrBlobNotFound) {
			http.Error(w, "blob not found", http.StatusNotFound)
			return
		}
		obs.ServerError(w, "blob read failed", err)
		http.Error(w, "blob read failed", http.StatusInternalServerError)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setContentLength(w.Header(), size)
	_, _ = io.Copy(w, rc)
}

// blobPresignTTL bounds a presigned download URL: long enough to start the
// download, short enough that a leaked URL is useless soon after.
const blobPresignTTL = 5 * time.Minute

type blobPresignRequest struct {
	Key         string `json:"key"`
	FileName    string `json:"fileName,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

type blobPresignResult struct {
	URL         string `json:"url"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// handleInternalBlobPresign answers POST /internal/v1/blobs/presign
// {key, fileName?, contentType?} with a short-lived direct download URL of
// the blob (the response carries the given type and an attachment
// disposition with the file name). It is 404 {"error":"direct downloads
// disabled"} unless BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS is on and the
// store can presign (S3); the caller then streams the bytes itself. Like the
// other /internal/v1/blobs routes it trusts only the svcauth signature: the
// caller has already authorized the read.
func (s *Service) handleInternalBlobPresign(w http.ResponseWriter, r *http.Request) {
	presigner, ok := s.Blobs.(blobstore.Presigner)
	if !s.DirectDownloads || !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "direct downloads disabled"})
		return
	}
	var req blobPresignRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	opts := blobstore.PresignOptions{ContentType: strings.TrimSpace(req.ContentType)}
	if opts.ContentType != "" {
		if _, _, err := mime.ParseMediaType(opts.ContentType); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid contentType"})
			return
		}
	}
	opts.ContentDisposition = httpx.ContentDisposition("attachment", strings.TrimSpace(req.FileName))
	expires := time.Now().Add(blobPresignTTL)
	u, err := presigner.PresignGet(r.Context(), strings.TrimSpace(req.Key), blobPresignTTL, opts)
	if errors.Is(err, blobstore.ErrInvalidKey) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid blob key"})
		return
	}
	if err != nil {
		obs.ServerError(w, "blob presign failed", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "blob presign failed"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, blobPresignResult{URL: u, ExpiresAtMs: expires.UnixMilli()})
}

// setContentLength declares a known blob size, so a reader (and every proxy
// in between) can tell a cut transfer from a complete one.
func setContentLength(h http.Header, size int64) {
	if size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
	}
}

func (s *Service) blobUnavailableMessage() string {
	if strings.TrimSpace(s.BlobError) != "" {
		return "blob store not configured: " + s.BlobError
	}
	return "blob store not configured"
}
