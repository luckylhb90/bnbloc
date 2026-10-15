package realtimehttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/attachment"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// maxAttachmentField is the multipart overhead an upload may add to its
// file (the part's headers, the boundary).
const maxAttachmentField = attachment.MaxNameLen

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
	checked, err := attachment.Check(header.Filename, header.Header.Get("Content-Type"), file)
	if msg, refused := attachment.Refusal(err); refused {
		httpx.Error(w, msg, http.StatusUnsupportedMediaType)
		return
	}
	if err != nil {
		httpx.Error(w, "attachment read failed", http.StatusBadRequest)
		return
	}
	filename, contentType, body := checked.Name, checked.Type.MIME, checked.Body
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
	attachment.ServeHeaders(w.Header(), att.Filename, att.ContentType)
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
