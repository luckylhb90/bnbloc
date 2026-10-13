package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const maxAttachmentField = 512

// attachmentType resolves an upload's type from wire.AttachmentTypes (the
// one list, which the composer's picker offers too): by the file's
// extension, else — a name without one — by its declared Content-Type. The
// declared type never overrides the name, and neither is trusted without
// attachmentContentMatches.
func attachmentType(filename, declared string) (wire.AttachmentType, bool) {
	ext := strings.ToLower(filepath.Ext(filename))
	for _, t := range wire.AttachmentTypes {
		if ext != "" && slices.Contains(t.Extensions, ext) {
			return t, true
		}
		if ext == "" && t.MIME == mediaType(declared) {
			return t, true
		}
	}
	return wire.AttachmentType{}, false
}

// oleMagic opens an OLE compound file (legacy Office documents).
var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// attachmentContentMatches reports whether an upload's first bytes pass its
// type's content check (wire.AttachmentContent*). Text must be text, not
// binary: markup is text too (a README opening with <!-- toc --> or
// <p align="center">), and a text attachment is only ever served as an
// opaque, nosniff, sandboxed download, so its markup never renders.
func attachmentContentMatches(content string, head []byte) bool {
	sniffed := mediaType(http.DetectContentType(head))
	switch content {
	case wire.AttachmentContentPNG:
		return sniffed == "image/png"
	case wire.AttachmentContentJPEG:
		return sniffed == "image/jpeg"
	case wire.AttachmentContentGIF:
		return sniffed == "image/gif"
	case wire.AttachmentContentWebP:
		return sniffed == "image/webp"
	case wire.AttachmentContentPDF:
		return sniffed == "application/pdf"
	case wire.AttachmentContentZip:
		return bytes.HasPrefix(head, []byte("PK\x03\x04")) || bytes.HasPrefix(head, []byte("PK\x05\x06"))
	case wire.AttachmentContentOLE:
		return bytes.HasPrefix(head, oleMagic)
	case wire.AttachmentContentText:
		return strings.HasPrefix(sniffed, "text/")
	case wire.AttachmentContentISOMedia:
		return len(head) >= 12 && string(head[4:8]) == "ftyp"
	case wire.AttachmentContentWebM:
		return bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3})
	case wire.AttachmentContentMP3:
		return bytes.HasPrefix(head, []byte("ID3")) || (len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0)
	case wire.AttachmentContentWAV:
		return len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WAVE"
	case wire.AttachmentContentOgg:
		return bytes.HasPrefix(head, []byte("OggS"))
	}
	return false
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
	roomID, userID, _, ok := s.room(w, r, membership.AnyMember)
	if !ok {
		return
	}
	if !s.attachmentsReady() {
		httpx.Unavailable(w, "attachments")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, wire.MaxAttachmentBytes+maxAttachmentField)
	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.Error(w, "expected a multipart 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()
	filename := sanitizeFilename(header.Filename)
	kind, ok := attachmentType(filename, header.Header.Get("Content-Type"))
	if !ok {
		httpx.Error(w, fmt.Sprintf("%s can't be attached: images, PDF, Office and OpenDocument files, text, audio, video and zip can", filename), http.StatusUnsupportedMediaType)
		return
	}
	contentType := kind.MIME
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		httpx.Error(w, "attachment read failed", http.StatusBadRequest)
		return
	}
	head = head[:n]
	if !attachmentContentMatches(kind.Content, head) {
		httpx.Error(w, fmt.Sprintf("%s can't be attached: its content is not a %s file", filename, kind.Extensions[0]), http.StatusUnsupportedMediaType)
		return
	}
	body := io.MultiReader(bytes.NewReader(head), file)
	key, size, sha, err := s.Blobs.Put(r.Context(), io.LimitReader(body, wire.MaxAttachmentBytes+1))
	if err != nil {
		obs.ServerError(w, "attachment store failed", err)
		httpx.Error(w, "attachment store failed", http.StatusInternalServerError)
		return
	}
	if size > wire.MaxAttachmentBytes {
		httpx.Error(w, "attachment too large", http.StatusRequestEntityTooLarge)
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
		httpx.Error(w, "attachment metadata store failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(attachmentMeta(att))
}

// handleDownloadAttachment streams a stored attachment to a room member.
func (s *Service) handleDownloadAttachment(w http.ResponseWriter, r *http.Request) {
	roomID, _, _, ok := s.room(w, r, membership.AnyMember)
	if !ok {
		return
	}
	if !s.attachmentsReady() {
		httpx.Unavailable(w, "attachments")
		return
	}
	att, ok, err := s.Attachments.GetAttachment(r.Context(), roomID, strings.TrimSpace(r.PathValue("attachmentId")))
	if err != nil {
		obs.ServerError(w, "attachment lookup failed", err)
		httpx.Error(w, "attachment lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, "attachment not found", http.StatusNotFound)
		return
	}
	blob, size, err := s.Blobs.Get(r.Context(), att.BlobKey)
	if err != nil {
		httpx.Error(w, "attachment blob missing", http.StatusNotFound)
		return
	}
	defer blob.Close()
	writeAttachmentHeaders(w.Header(), att)
	setContentLength(w.Header(), size)
	_, _ = io.Copy(w, blob)
}

// handleAttachmentMeta answers a stored attachment's metadata to a room
// member (wire.ChatAttachment: its name, declared type and size), as a
// chat message embeds it: what a work's recipe records of a request's
// attachment (docs/prd/2026-10-13-works-gallery-and-make-similar.md §3).
func (s *Service) handleAttachmentMeta(w http.ResponseWriter, r *http.Request) {
	roomID, _, _, ok := s.room(w, r, membership.AnyMember)
	if !ok {
		return
	}
	if !s.attachmentsReady() {
		httpx.Unavailable(w, "attachments")
		return
	}
	att, ok, err := s.Attachments.GetAttachment(r.Context(), roomID, strings.TrimSpace(r.PathValue("attachmentId")))
	if err != nil {
		obs.ServerError(w, "attachment lookup failed", err)
		httpx.Error(w, "attachment lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, "attachment not found", http.StatusNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, attachmentMeta(att))
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
func (s *Service) validateAttachmentIDs(ctx context.Context, roomID string, ids []string) ([]wire.ChatAttachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if !s.attachmentsReady() {
		return nil, errors.New("attachments not configured")
	}
	out := make([]wire.ChatAttachment, 0, len(ids))
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

func attachmentMeta(a store.Attachment) wire.ChatAttachment {
	return wire.ChatAttachment{ID: a.ID, Filename: a.Filename, ContentType: a.ContentType, Size: a.Size}
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
