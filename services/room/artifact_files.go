package room

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// Binary-capable artifacts (docs/prd/2026-10-09-preview-workbench-and-
// previewer-components.md, "Contracts"): a version is inline text or bytes in
// realtime's content-addressed blob store; members fetch any version's raw
// bytes through the content route and store/fetch per-version thumbnails.

// ArtifactBlobs is the room service's port onto the blob store
// (internal/realtime.HandoffClient implements it over realtime's signed
// internal blob route). Every call acts as the end user.
type ArtifactBlobs interface {
	PutBlob(ctx context.Context, user string, content io.Reader) (realtime.BlobRef, error)
	OpenBlob(ctx context.Context, user, key string) (io.ReadCloser, int64, error)
	// PresignBlob returns a short-lived direct download URL, or
	// realtime.ErrDirectDownloadsDisabled when the store hands out none.
	PresignBlob(ctx context.Context, user, key, fileName, contentType string) (realtime.BlobPresign, error)
}

// DefaultArtifactMaxBytes is the documented default blob version cap.
const DefaultArtifactMaxBytes int64 = 100 << 20

// maxArtifactBlobBytes is the largest object realtime's blob store accepts in
// one put; a configured cap above it could never be honoured.
const maxArtifactBlobBytes int64 = 256 << 20

// ArtifactMaxBytesFromEnv reads BNBLOC_ARTIFACT_MAX_BYTES (bytes; unset =
// DefaultArtifactMaxBytes). A malformed value or one outside
// 1..256 MiB is a startup error, not a silent fallback.
func ArtifactMaxBytesFromEnv() (int64, error) {
	raw := strings.TrimSpace(config.FromEnvRaw("BNBLOC_ARTIFACT_MAX_BYTES"))
	if raw == "" {
		return DefaultArtifactMaxBytes, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || n > maxArtifactBlobBytes {
		return 0, fmt.Errorf("BNBLOC_ARTIFACT_MAX_BYTES=%q: want a byte count in 1..%d", raw, maxArtifactBlobBytes)
	}
	return n, nil
}

func (s *Service) artifactMaxBytes() int64 {
	if s.ArtifactMaxBytes > 0 {
		return s.ArtifactMaxBytes
	}
	return DefaultArtifactMaxBytes
}

// Thumbnail bounds (contract): PNG or WebP, ≤ 512 KiB, ≤ 1024 px per side.
const (
	maxThumbnailBytes = 512 << 10
	maxThumbnailSide  = 1024
)

// artifactCSP sandboxes any artifact bytes a browser does render, so even a
// mis-typed file cannot run script with the app origin's privileges.
const artifactCSP = "default-src 'none'; sandbox"

// maxMultipartFieldBytes bounds one non-file form field of a blob publish.
const maxMultipartFieldBytes = 4 << 10

// sanitizeArtifactFileName keeps a display file name: the last path element,
// trimmed, without control characters.
func sanitizeArtifactFileName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	if !utf8.ValidString(name) {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
}

// errBlobTooLarge marks a file part that exceeded the cap mid-stream.
var errBlobTooLarge = errors.New("artifact file exceeds the size limit")

// cappedReader fails the stream once more than max bytes were read, so an
// over-cap upload aborts the blob put instead of storing a truncated blob.
type cappedReader struct {
	r        io.Reader
	n, max   int64
	exceeded bool
}

func (c *cappedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.max {
		c.exceeded = true
		return n, errBlobTooLarge
	}
	return n, err
}

// blobUpload is a parsed multipart blob publish.
type blobUpload struct {
	ref         realtime.BlobRef
	mimeType    string
	fileName    string
	baseVersion int
	hasBase     bool
	summary     string
	sourceJobID string
	sourceSeq   int64
}

// publishBlobVersion handles the multipart form of POST …/versions: the
// "file" part streams into the blob store (never buffered whole), typed by
// its declared Content-Type, else its file name, else sniffing; fields
// baseVersion, summary, sourceJobId, sourceSeq may come in any order. A
// declared raster image type must match the sniffed bytes (those are the only
// types served inline). Re-publishing the current version's exact bytes
// (same sha256) is a no-op answered with unchanged=true.
func (s *Service) publishBlobVersion(w http.ResponseWriter, r *http.Request, art store.Artifact, userID string) {
	if s.Blobs == nil {
		obs.ServerError(w, "artifact blob store not configured", nil)
		writeErr(w, http.StatusServiceUnavailable, "artifact blob store not configured")
		return
	}
	max := s.artifactMaxBytes()
	r.Body = http.MaxBytesReader(w, r.Body, max+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid multipart body")
		return
	}
	var up blobUpload
	seenFile := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.writeUploadErr(w, err, max)
			return
		}
		name := part.FormName()
		if name == "file" {
			if seenFile {
				writeErr(w, http.StatusBadRequest, "exactly one file part is allowed")
				return
			}
			seenFile = true
			code, msg := s.storeFilePart(r.Context(), userID, art, part, max, &up)
			if code != 0 {
				if code >= 500 {
					obs.ServerError(w, msg, nil)
				}
				writeErr(w, code, msg)
				return
			}
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(part, maxMultipartFieldBytes+1))
		if err != nil {
			s.writeUploadErr(w, err, max)
			return
		}
		if len(raw) > maxMultipartFieldBytes {
			writeErr(w, http.StatusBadRequest, "form field "+name+" is too long")
			return
		}
		val := strings.TrimSpace(string(raw))
		switch name {
		case "baseVersion":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				writeErr(w, http.StatusBadRequest, "baseVersion must be >= 0")
				return
			}
			up.baseVersion, up.hasBase = n, true
		case "summary":
			up.summary = val
		case "sourceJobId":
			up.sourceJobID = val
		case "sourceSeq":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid sourceSeq")
				return
			}
			up.sourceSeq = n
		}
	}
	if !seenFile {
		writeErr(w, http.StatusBadRequest, "expected a multipart 'file' part")
		return
	}
	if !up.hasBase {
		writeErr(w, http.StatusBadRequest, "baseVersion is required")
		return
	}
	if !validVersionProvenance(w, up.summary, up.sourceJobID, up.sourceSeq) {
		return
	}
	// Dedupe: the same bytes as the current version are not a new revision.
	if art.CurrentVersion > 0 {
		cur, err := s.Artifacts.GetArtifactVersion(r.Context(), art.ArtifactID, art.CurrentVersion)
		if err == nil && cur.ContentHash == up.ref.SHA256 && cur.MimeType == up.mimeType {
			cur.Content = ""
			writeJSON(w, http.StatusOK, map[string]any{"artifact": art, "version": cur, "unchanged": true})
			return
		}
	}
	s.commitVersion(w, r, art, userID, store.ArtifactVersion{
		Storage: store.ArtifactStorageBlob, BlobKey: up.ref.Key, ContentHash: up.ref.SHA256,
		SizeBytes: up.ref.Size, MimeType: up.mimeType,
		SourceJobID: up.sourceJobID, SourceSeq: up.sourceSeq,
		Summary: up.summary, AuthorUserID: userID, CreatedAtMs: s.Now(),
	}, up.baseVersion)
}

// storeFilePart types and streams the file part into the blob store. It
// returns a non-zero status and message on refusal.
func (s *Service) storeFilePart(ctx context.Context, userID string, art store.Artifact, part *multipart.Part, max int64, up *blobUpload) (int, string) {
	up.fileName = sanitizeArtifactFileName(part.FileName())
	declared := store.MediaType(part.Header.Get("Content-Type"))
	head := make([]byte, 512)
	n, err := io.ReadFull(part, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return http.StatusBadRequest, "artifact file read failed"
	}
	head = head[:n]
	name := firstNonEmptyString(up.fileName, art.FileName, art.Title)
	mimeType := declared
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = store.ArtifactMIME(name, head)
		if art.Kind == store.ArtifactKindSite {
			mimeType = tarbundle.SiteMIME
		}
	}
	if siteTypeMismatch(art.Kind, mimeType) {
		return http.StatusUnsupportedMediaType, "a site version must be " + tarbundle.SiteMIME + ", and only a site may carry that type"
	}
	if _, _, err := mime.ParseMediaType(mimeType); err != nil || len(mimeType) > 128 {
		return http.StatusBadRequest, "invalid file content type"
	}
	if store.IsInlineSafeImage(mimeType) && store.MediaType(http.DetectContentType(head)) != mimeType {
		return http.StatusUnsupportedMediaType, "file content does not match its declared image type"
	}
	up.mimeType = mimeType
	capped := &cappedReader{r: io.MultiReader(bytes.NewReader(head), part), max: max}
	ref, err := s.Blobs.PutBlob(ctx, userID, capped)
	if capped.exceeded {
		return http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds the %d byte limit", max)
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds the %d byte limit", max)
	}
	if err != nil {
		return http.StatusBadGateway, "artifact blob store failed: " + err.Error()
	}
	up.ref = ref
	return 0, ""
}

func (s *Service) writeUploadErr(w http.ResponseWriter, err error, max int64) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds the %d byte limit", max))
		return
	}
	writeErr(w, http.StatusBadRequest, "invalid multipart body")
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// artifactVersionForRead resolves the path's artifact + version for a member
// read; it writes the refusal and returns ok=false otherwise.
func (s *Service) artifactVersionForRead(w http.ResponseWriter, r *http.Request) (store.Artifact, store.ArtifactVersion, bool) {
	roomID := r.PathValue("roomId")
	if !s.Store.IsMember(r.Context(), roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return store.Artifact{}, store.ArtifactVersion{}, false
	}
	art, err := s.Artifacts.GetArtifact(r.Context(), r.PathValue("artifactId"))
	if err != nil || art.RoomID != roomID {
		writeErr(w, http.StatusNotFound, "artifact not found")
		return store.Artifact{}, store.ArtifactVersion{}, false
	}
	version, err := strconv.Atoi(strings.TrimSpace(r.PathValue("version")))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid version")
		return store.Artifact{}, store.ArtifactVersion{}, false
	}
	v, err := s.Artifacts.GetArtifactVersion(r.Context(), art.ArtifactID, version)
	if err != nil {
		writeErr(w, http.StatusNotFound, "version not found")
		return store.Artifact{}, store.ArtifactVersion{}, false
	}
	return art, v, true
}

// handleArtifactContent serves one version's raw bytes (inline text or blob)
// with its MIME type. Every response is nosniff and CSP-sandboxed; only
// sniff-verified raster images are inline, everything else is an attachment
// (viewers fetch the bytes, so disposition only matters to a direct
// navigation). Versions are immutable, so the bytes cache by content hash.
// Unlike realtime's attachment download, which forces octet-stream for
// non-images, artifact viewers need the real type to pick a renderer.
// ?download=1 (a download navigation) answers a blob version with a 302 to a
// presigned URL when realtime hands them out; otherwise it streams as usual.
func (s *Service) handleArtifactContent(w http.ResponseWriter, r *http.Request) {
	art, v, ok := s.artifactVersionForRead(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("download") == "1" && v.Storage == store.ArtifactStorageBlob && s.Blobs != nil {
		p, err := s.Blobs.PresignBlob(r.Context(), caller(r), v.BlobKey, store.ArtifactDownloadName(art.Kind, art.FileName, art.Title), v.MimeType)
		switch {
		case err == nil:
			w.Header().Set("Cache-Control", "no-store")
			// The download URL may carry the session (access_token): never
			// hand it to the storage host as a Referer.
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.Redirect(w, r, p.URL, http.StatusFound)
			return
		case !errors.Is(err, realtime.ErrDirectDownloadsDisabled):
			obs.ServerError(w, "artifact presign failed", err)
			writeErr(w, http.StatusBadGateway, "artifact presign failed")
			return
		}
	}
	mimeType := v.MimeType
	var body io.Reader
	size := v.SizeBytes
	switch v.Storage {
	case store.ArtifactStorageBlob:
		if s.Blobs == nil {
			obs.ServerError(w, "artifact blob store not configured", nil)
			writeErr(w, http.StatusServiceUnavailable, "artifact blob store not configured")
			return
		}
		rc, n, err := s.Blobs.OpenBlob(r.Context(), caller(r), v.BlobKey)
		if err != nil {
			writeBlobReadErr(w, err)
			return
		}
		defer rc.Close()
		body, size = rc, n
	default:
		if mimeType == "" {
			mimeType = store.ArtifactInlineMIME(art)
		}
		body, size = strings.NewReader(v.Content), int64(len(v.Content))
	}
	h := w.Header()
	fileName := store.ArtifactDownloadName(art.Kind, art.FileName, art.Title)
	writeArtifactBytesHeaders(h, mimeType, fileName)
	if r.URL.Query().Get("download") == "1" {
		// A download navigation saves even an inline-safe image.
		h.Set("Content-Disposition", httpx.ContentDisposition("attachment", fileName))
	}
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("ETag", `"`+v.ContentHash+`"`)
	if size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if r.Header.Get("If-None-Match") == `"`+v.ContentHash+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.Copy(w, body)
}

// writeArtifactBytesHeaders sets the safe serving headers for artifact bytes.
func writeArtifactBytesHeaders(h http.Header, mimeType, fileName string) {
	mt := store.MediaType(mimeType)
	if mt == "" {
		mt = "application/octet-stream"
	}
	ct := mt
	if strings.HasPrefix(mt, "text/") || mt == "application/json" {
		ct = mt + "; charset=utf-8"
	}
	h.Set("Content-Type", ct)
	disposition := "attachment"
	if store.IsInlineSafeImage(mt) {
		disposition = "inline"
	}
	h.Set("Content-Disposition", httpx.ContentDisposition(disposition, fileName))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", artifactCSP)
}

// handlePutArtifactThumbnail stores a version thumbnail (any member). The
// body is the raw PNG or WebP (Content-Type image/png | image/webp), verified
// by magic bytes and header dimensions. ?source=builtin (default) |
// previewer ranks the writer: the first thumbnail wins, and a later one
// replaces it only from a higher-ranked source; a refused write answers 200
// with stored=false and the kept thumbnail.
func (s *Service) handlePutArtifactThumbnail(w http.ResponseWriter, r *http.Request) {
	art, v, ok := s.artifactVersionForRead(w, r)
	if !ok {
		return
	}
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		source = store.ArtifactThumbnailBuiltin
	}
	rank := store.ArtifactThumbnailRank(source)
	if rank == 0 {
		writeErr(w, http.StatusBadRequest, "source must be builtin or previewer")
		return
	}
	mimeType := store.MediaType(r.Header.Get("Content-Type"))
	if mimeType != "image/png" && mimeType != "image/webp" {
		writeErr(w, http.StatusUnsupportedMediaType, "thumbnail must be image/png or image/webp")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxThumbnailBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "thumbnail read failed")
		return
	}
	if len(raw) > maxThumbnailBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "thumbnail exceeds 512 KiB")
		return
	}
	width, height, err := thumbnailDimensions(mimeType, raw)
	if err != nil {
		writeErr(w, http.StatusUnsupportedMediaType, "thumbnail is not a valid "+mimeType+": "+err.Error())
		return
	}
	if width <= 0 || height <= 0 || width > maxThumbnailSide || height > maxThumbnailSide {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("thumbnail must be at most %dx%d px", maxThumbnailSide, maxThumbnailSide))
		return
	}
	if v.ThumbnailBlobKey != "" && v.ThumbnailRank >= rank {
		writeJSON(w, http.StatusOK, map[string]any{"version": metadataOnly(v), "stored": false})
		return
	}
	if s.Blobs == nil {
		obs.ServerError(w, "artifact blob store not configured", nil)
		writeErr(w, http.StatusServiceUnavailable, "artifact blob store not configured")
		return
	}
	ref, err := s.Blobs.PutBlob(r.Context(), caller(r), bytes.NewReader(raw))
	if err != nil {
		obs.ServerError(w, "thumbnail store failed", err)
		writeErr(w, http.StatusBadGateway, "thumbnail store failed")
		return
	}
	stored, landed, err := s.Artifacts.SetArtifactThumbnail(r.Context(), art.ArtifactID, v.Version, store.ArtifactThumbnailWrite{
		BlobKey: ref.Key, MimeType: mimeType, Width: width, Height: height, Rank: rank,
	})
	if err != nil {
		obs.ServerError(w, "thumbnail record failed", err)
		writeErr(w, http.StatusInternalServerError, "thumbnail record failed")
		return
	}
	if landed {
		s.publishArtifactEventReason(r.Context(), art, caller(r), "thumbnail")
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": metadataOnly(stored), "stored": landed})
}

// handleGetArtifactThumbnail serves a version's stored thumbnail (404 when
// none). A higher-ranked thumbnail may replace it, so it revalidates by ETag
// instead of caching as immutable.
func (s *Service) handleGetArtifactThumbnail(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.artifactVersionForRead(w, r)
	if !ok {
		return
	}
	if v.ThumbnailBlobKey == "" || v.Thumbnail == nil {
		writeErr(w, http.StatusNotFound, "no thumbnail")
		return
	}
	etag := `"` + strings.TrimPrefix(v.ThumbnailBlobKey, "sha256/") + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if s.Blobs == nil {
		obs.ServerError(w, "artifact blob store not configured", nil)
		writeErr(w, http.StatusServiceUnavailable, "artifact blob store not configured")
		return
	}
	rc, n, err := s.Blobs.OpenBlob(r.Context(), caller(r), v.ThumbnailBlobKey)
	if err != nil {
		writeBlobReadErr(w, err)
		return
	}
	defer rc.Close()
	h := w.Header()
	writeArtifactBytesHeaders(h, v.Thumbnail.MimeType, "")
	h.Set("Content-Disposition", "inline")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", etag)
	if n >= 0 {
		h.Set("Content-Length", strconv.FormatInt(n, 10))
	}
	_, _ = io.Copy(w, rc)
}

// writeBlobReadErr maps a blob store read failure: a blob the store no
// longer holds is a 404 (visible, not a gateway fault); anything else is a
// backend failure.
func writeBlobReadErr(w http.ResponseWriter, err error) {
	var se *realtime.StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		writeErr(w, http.StatusNotFound, "artifact bytes are no longer stored")
		return
	}
	obs.ServerError(w, "artifact blob read failed", err)
	writeErr(w, http.StatusBadGateway, "artifact blob read failed")
}

func metadataOnly(v store.ArtifactVersion) store.ArtifactVersion {
	v.Content = ""
	return v
}

// thumbnailDimensions validates a PNG or WebP by its magic bytes and returns
// the header dimensions (no pixel decode: the header is all a size check
// needs, and decoding untrusted images is the risk being avoided).
func thumbnailDimensions(mimeType string, b []byte) (int, int, error) {
	switch mimeType {
	case "image/png":
		if !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
			return 0, 0, errors.New("missing PNG signature")
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	case "image/webp":
		return webpDimensions(b)
	}
	return 0, 0, errors.New("unsupported type")
}

// webpDimensions reads the canvas size from a WebP's first chunk (lossy VP8,
// lossless VP8L, or extended VP8X).
func webpDimensions(b []byte) (int, int, error) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, errors.New("missing WebP signature")
	}
	if int(binary.LittleEndian.Uint32(b[4:8]))+8 > len(b) {
		return 0, 0, errors.New("truncated WebP")
	}
	switch string(b[12:16]) {
	case "VP8 ":
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, errors.New("bad VP8 start code")
		}
		w := int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
		return w, h, nil
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0, errors.New("bad VP8L signature")
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, nil
	case "VP8X":
		w := int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1
		h := int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1
		return w, h, nil
	}
	return 0, 0, errors.New("unknown WebP chunk")
}
