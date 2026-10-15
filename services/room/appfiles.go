package room

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/attachment"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// App files (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md §6a):
// a file an app's page uploads (a photo, a receipt) next to its JSON
// documents. What a file may be and how it is served is internal/attachment's
// (the chat attachments' one owner); its bytes go to the blob store, its
// name and uploader are sealed under the app's key; the two rules hold as
// for a document: readable by its uploader and the app's owners unless
// uploaded public, deleted by its uploader or an owner. A file belongs to
// the app's data: a wipe (and the room's deletion) deletes it.
//
//	POST   /internal/v1/app/files                ?public=1, multipart "file" → 201 wire.AppFile
//	GET    /internal/v1/app/files/{id}           its metadata                → wire.AppFile
//	GET    /internal/v1/app/files/{id}/content   its bytes (attachment.ServeHeaders)
//	DELETE /internal/v1/app/files/{id}                                       → 204

func (s *Service) registerAppFileRoutes(route func(pattern string, need appNeed, h appHandler)) {
	route("POST /internal/v1/app/files", appWrite, s.handleAppFileUpload)
	route("GET /internal/v1/app/files/{id}", appRead, s.handleAppFileMeta)
	route("GET /internal/v1/app/files/{id}/content", appRead, s.handleAppFileContent)
	route("DELETE /internal/v1/app/files/{id}", appRead, s.handleAppFileDelete)
}

// admitFile checks one more file of size bytes against the app's file caps.
func (c appCaps) admitFile(u store.AppCounts, size int64) error {
	if c.files == 0 {
		c.files, c.fileBytes = wire.MaxAppFiles, wire.MaxAppFileBytes
	}
	switch {
	case u.Files+1 > c.files:
		return fmt.Errorf("%w: %d files", errAppFull, c.files)
	case u.FileBytes+size > c.fileBytes:
		return fmt.Errorf("%w: %d bytes of files", errAppFull, c.fileBytes)
	}
	return nil
}

// appFilePart returns the multipart body's "file" part (its name and
// declared type), or an error a person reads.
func appFilePart(r *http.Request) (io.Reader, string, string, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", "", appdataInvalid("expected a multipart body with a 'file' part")
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			return nil, "", "", appdataInvalid("expected a multipart body with a 'file' part")
		}
		if p.FormName() == "file" {
			return p, p.FileName(), p.Header.Get("Content-Type"), nil
		}
	}
}

// handleAppFileUpload stores one file by the caller: checked like a chat
// attachment, at most wire.MaxAttachmentBytes, within the app's file caps.
func (s *Service) handleAppFileUpload(w http.ResponseWriter, r *http.Request, a *appCall) {
	if s.Blobs == nil {
		httpx.Unavailable(w, "app files")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, wire.MaxAppUploadBytes)
	public := r.URL.Query().Get("public") == "1"
	part, name, declared, err := appFilePart(r)
	if err != nil {
		writeAppError(w, err)
		return
	}
	checked, err := attachment.Check(name, declared, part)
	if msg, refused := attachment.Refusal(err); refused {
		httpx.Error(w, msg, http.StatusUnsupportedMediaType)
		return
	}
	if err != nil {
		writeAppError(w, appdataInvalid("the file could not be read"))
		return
	}
	usage, err := s.appUsage.snapshot(a.ctx, s.AppDocs, a.tenantID, a.scope, s.Now())
	if err != nil {
		writeAppError(w, err)
		return
	}
	if err := s.appCaps.admitFile(usage, 0); err != nil {
		writeAppError(w, err)
		return
	}
	ref, err := s.Blobs.PutBlob(a.ctx, io.LimitReader(checked.Body, wire.MaxAttachmentBytes+1))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAppError(w, fmt.Errorf("%w: a file is at most %d bytes", appdata.ErrTooLarge, wire.MaxAttachmentBytes))
			return
		}
		obs.ServerError(w, "store app file failed", err)
		httpx.Error(w, "store app file failed", http.StatusBadGateway)
		return
	}
	if ref.Size > wire.MaxAttachmentBytes {
		writeAppError(w, fmt.Errorf("%w: a file is at most %d bytes", appdata.ErrTooLarge, wire.MaxAttachmentBytes))
		return
	}
	if err := s.appCaps.admitFile(usage, ref.Size); err != nil {
		writeAppError(w, err)
		return
	}
	for attempt := 0; ; attempt++ {
		f := store.AppFile{AppID: a.scope.AppID, FileID: ids.New("f"), RoomID: a.scope.RoomID, ByKey: a.me, Public: public,
			ContentType: checked.Type.MIME, Size: ref.Size, BlobKey: ref.Key, CreatedAtMs: s.Now()}
		if err := a.key.SealFile(&f, checked.Name, a.participant); err != nil {
			writeAppError(w, err)
			return
		}
		err := s.AppDocs.InsertAppFile(a.ctx, f, a.wrapped)
		if errors.Is(err, store.ErrAppKeyChanged) && attempt == 0 {
			if err := s.loadAppKey(a, true); err != nil {
				writeAppError(w, err)
				return
			}
			continue
		}
		if err != nil {
			writeAppError(w, err)
			return
		}
		s.appUsage.addFile(a.tenantID, a.scope, ref.Size)
		s.writeAppFile(w, a, http.StatusCreated, f)
		return
	}
}

// appFile reads the file of the request's path the caller may read (rule
// 2): anyone else's private file is not found.
func (s *Service) appFile(r *http.Request, a *appCall) (store.AppFile, error) {
	id := r.PathValue("id")
	if !appdata.ValidDocID(id) {
		return store.AppFile{}, appdataInvalid("file id")
	}
	if a.key == nil {
		return store.AppFile{}, store.ErrAppDocNotFound
	}
	f, ok, err := s.AppDocs.GetAppFile(a.ctx, store.AppFileRef{AppScope: a.scope, FileID: id})
	if err != nil {
		return store.AppFile{}, err
	}
	if !ok || !(a.owner || f.Public || (a.me != "" && f.ByKey == a.me)) {
		return store.AppFile{}, store.ErrAppDocNotFound
	}
	return f, nil
}

// fileView is a stored file as the caller reads it.
func (a *appCall) fileView(f store.AppFile) (wire.AppFile, error) {
	name, err := a.key.FileName(f)
	if err != nil {
		return wire.AppFile{}, err
	}
	out := wire.AppFile{ID: f.FileID, Name: name, Type: f.ContentType, Size: f.Size, Public: f.Public, By: f.ByKey, CreatedAt: f.CreatedAtMs}
	if a.owner {
		if out.Author, err = a.key.FileAuthor(f); err != nil {
			return wire.AppFile{}, err
		}
	}
	return out, nil
}

func (s *Service) writeAppFile(w http.ResponseWriter, a *appCall, status int, f store.AppFile) {
	v, err := a.fileView(f)
	if err != nil {
		writeAppError(w, err)
		return
	}
	httpx.WriteJSON(w, status, v)
}

func (s *Service) handleAppFileMeta(w http.ResponseWriter, r *http.Request, a *appCall) {
	f, err := s.appFile(r, a)
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.writeAppFile(w, a, http.StatusOK, f)
}

// handleAppFileContent streams a file's bytes, served as a chat attachment
// is: only verified raster images inline, everything else an opaque,
// sandboxed download.
func (s *Service) handleAppFileContent(w http.ResponseWriter, r *http.Request, a *appCall) {
	if s.Blobs == nil {
		httpx.Unavailable(w, "app files")
		return
	}
	f, err := s.appFile(r, a)
	if err != nil {
		writeAppError(w, err)
		return
	}
	name, err := a.key.FileName(f)
	if err != nil {
		writeAppError(w, err)
		return
	}
	rc, size, err := s.Blobs.OpenBlob(a.ctx, f.BlobKey)
	if err != nil {
		writeBlobReadErr(w, err)
		return
	}
	defer rc.Close()
	attachment.ServeHeaders(w.Header(), name, f.ContentType)
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	_, _ = io.Copy(w, rc)
}

// handleAppFileDelete deletes a file (rule 1: its uploader or an owner).
// The documents that name it are the app's: they are left as they are.
func (s *Service) handleAppFileDelete(w http.ResponseWriter, r *http.Request, a *appCall) {
	id := r.PathValue("id")
	if !appdata.ValidDocID(id) {
		writeAppError(w, appdataInvalid("file id"))
		return
	}
	if a.key == nil {
		writeAppError(w, store.ErrAppDocNotFound)
		return
	}
	var size int64
	err := s.AppDocs.DeleteAppFile(a.ctx, store.AppFileRef{AppScope: a.scope, FileID: id}, func(cur store.AppFile) error {
		size = cur.Size
		switch {
		case a.owner || (a.me != "" && cur.ByKey == a.me):
			return nil
		case cur.Public:
			return errAppNotYours
		}
		return store.ErrAppDocNotFound
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.appUsage.addFile(a.tenantID, a.scope, -size)
	w.WriteHeader(http.StatusNoContent)
}
