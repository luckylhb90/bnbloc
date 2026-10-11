package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

const (
	maxAttachmentBytes = 25 * 1024 * 1024
	maxAttachmentField = 512
)

// allowedAttachmentTypes gates uploads to passive images and common documents,
// mapping each accepted client-declared type to the check its server-sniffed
// content (http.DetectContentType over the first 512 bytes) must pass. The
// declared type alone is never trusted: an HTML or script payload declared as
// image/png or text/plain is refused. Active formats (SVG, HTML, XML) are not
// accepted at all — served from the app origin they execute script.
var allowedAttachmentTypes = map[string]func(sniffed string) bool{
	"image/png":        sniffedAs("image/png"),
	"image/jpeg":       sniffedAs("image/jpeg"),
	"image/gif":        sniffedAs("image/gif"),
	"image/webp":       sniffedAs("image/webp"),
	"application/pdf":  sniffedAs("application/pdf"),
	"application/zip":  sniffedAs("application/zip"),
	"text/plain":       sniffedPlainText,
	"text/markdown":    sniffedPlainText,
	"text/csv":         sniffedPlainText,
	"application/json": sniffedPlainText,
}

// inlineAttachmentTypes are the only types served inline (rendered by the
// browser): raster images, whose bytes were sniff-verified at upload. Every
// other attachment is served as an opaque download.
var inlineAttachmentTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// attachmentCSP sandboxes any attachment response that a browser does render,
// so even a mis-typed file cannot run script with the app origin's privileges.
const attachmentCSP = "default-src 'none'; sandbox"

func sniffedAs(want string) func(string) bool {
	return func(sniffed string) bool { return mediaType(sniffed) == want }
}

// sniffedPlainText accepts content the sniffer classifies as plain text; HTML
// or XML markup sniffs as text/html / text/xml and is refused.
func sniffedPlainText(sniffed string) bool { return mediaType(sniffed) == "text/plain" }

// AttachmentStore is the metadata port the realtime service needs (the store
// package's concrete impl satisfies it).
type AttachmentStore interface {
	PutAttachment(ctx context.Context, a store.Attachment) error
	GetAttachment(ctx context.Context, roomID, id string) (store.Attachment, bool, error)
}

// BlobStore is the binary port (internal/blobstore satisfies it).
type BlobStore interface {
	Put(ctx context.Context, r io.Reader) (key string, size int64, sha256hex string, err error)
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

// mediaType lowercases a content type and strips its parameters.
func mediaType(ct string) string {
	ct = strings.TrimSpace(strings.ToLower(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

func (s *Service) attachmentsReady() bool {
	return s.Attachments != nil && s.Blobs != nil
}

// handleUploadAttachment streams one multipart file into the blob store and
// records its metadata against the room. Member-scoped like chat send.
func (s *Service) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	if s.Membership == nil || !s.Membership.IsMember(roomID, userID, tenant.FromContext(r.Context())) {
		http.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	if !s.attachmentsReady() {
		obs.ServerError(w, "attachments not configured", nil)
		http.Error(w, "attachments not configured", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes+maxAttachmentField)
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "expected a multipart 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()
	filename := sanitizeFilename(header.Filename)
	contentType := mediaType(header.Header.Get("Content-Type"))
	check, ok := allowedAttachmentTypes[contentType]
	if !ok {
		http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
		return
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		http.Error(w, "attachment read failed", http.StatusBadRequest)
		return
	}
	head = head[:n]
	if !check(http.DetectContentType(head)) {
		http.Error(w, "attachment content does not match its declared type", http.StatusUnsupportedMediaType)
		return
	}
	body := io.MultiReader(bytes.NewReader(head), file)
	key, size, sha, err := s.Blobs.Put(r.Context(), io.LimitReader(body, maxAttachmentBytes+1))
	if err != nil {
		obs.ServerError(w, "attachment store failed", err)
		http.Error(w, "attachment store failed", http.StatusInternalServerError)
		return
	}
	if size > maxAttachmentBytes {
		http.Error(w, "attachment too large", http.StatusRequestEntityTooLarge)
		return
	}
	att := store.Attachment{
		ID:          ids.New("att_"),
		RoomID:      roomID,
		Uploader:    userID,
		Filename:    filename,
		ContentType: contentType,
		Size:        size,
		SHA256:      sha,
		BlobKey:     key,
		CreatedAtMs: time.Now().UnixMilli(),
	}
	if err := s.Attachments.PutAttachment(r.Context(), att); err != nil {
		obs.ServerError(w, "attachment metadata store failed", err)
		http.Error(w, "attachment metadata store failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(attachmentMeta(att))
}

// handleDownloadAttachment streams a stored attachment to a room member.
func (s *Service) handleDownloadAttachment(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if userID == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	if s.Membership == nil || !s.Membership.IsMember(roomID, userID, tenant.FromContext(r.Context())) {
		http.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	if !s.attachmentsReady() {
		obs.ServerError(w, "attachments not configured", nil)
		http.Error(w, "attachments not configured", http.StatusServiceUnavailable)
		return
	}
	att, ok, err := s.Attachments.GetAttachment(r.Context(), roomID, strings.TrimSpace(r.PathValue("attachmentId")))
	if err != nil {
		obs.ServerError(w, "attachment lookup failed", err)
		http.Error(w, "attachment lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "attachment not found", http.StatusNotFound)
		return
	}
	blob, size, err := s.Blobs.Get(r.Context(), att.BlobKey)
	if err != nil {
		http.Error(w, "attachment blob missing", http.StatusNotFound)
		return
	}
	defer blob.Close()
	writeAttachmentHeaders(w.Header(), att)
	setContentLength(w.Header(), size)
	_, _ = io.Copy(w, blob)
}

// writeAttachmentHeaders sets the download response headers. Only verified
// raster images render inline with their type; everything else (including
// rows stored before upload sniffing existed, e.g. SVG) is an opaque
// application/octet-stream attachment. nosniff and a sandboxing CSP apply to
// every response, and the filename is RFC 2231-encoded so it cannot break out
// of the header value.
func writeAttachmentHeaders(h http.Header, att store.Attachment) {
	ct, disposition := "application/octet-stream", "attachment"
	if t := mediaType(att.ContentType); inlineAttachmentTypes[t] {
		ct, disposition = t, "inline"
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", httpx.ContentDisposition(disposition, att.Filename))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", attachmentCSP)
}

// validateAttachmentIDs confirms every id belongs to the room and returns their
// metadata for embedding in the chat_message payload.
func (s *Service) validateAttachmentIDs(ctx context.Context, roomID string, ids []string) ([]map[string]any, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if !s.attachmentsReady() {
		return nil, errors.New("attachments not configured")
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		att, ok, err := s.Attachments.GetAttachment(ctx, roomID, strings.TrimSpace(id))
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("unknown attachment " + id)
		}
		out = append(out, attachmentMeta(att))
	}
	return out, nil
}

func attachmentMeta(a store.Attachment) map[string]any {
	return map[string]any{
		"id":          a.ID,
		"filename":    a.Filename,
		"contentType": a.ContentType,
		"size":        a.Size,
	}
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	if len(name) > maxAttachmentField {
		name = name[len(name)-maxAttachmentField:]
	}
	if name == "" {
		return "file"
	}
	return name
}
