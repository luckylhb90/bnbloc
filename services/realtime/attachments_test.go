package realtimehttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// memBlobs is an in-memory BlobStore for attachment tests.
type memBlobs struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (b *memBlobs) Put(_ context.Context, r io.Reader) (string, int64, string, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return "", 0, "", err
	}
	sum := sha256.Sum256(buf)
	hexsum := hex.EncodeToString(sum[:])
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m["sha256/"+hexsum] = buf
	return "sha256/" + hexsum, int64(len(buf)), hexsum, nil
}

func (b *memBlobs) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	buf, ok := b.m[key]
	if !ok {
		return nil, 0, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(buf)), int64(len(buf)), nil
}

func newAttachmentServer(t *testing.T) (*httptest.Server, *Service) {
	t.Helper()
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	svc := &Service{
		Hub:         realtime.NewHub(16, 16),
		Membership:  mem,
		Attachments: store.NewMemoryAttachments(),
		Blobs:       &memBlobs{m: map[string][]byte{}},
	}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return srv, svc
}

func uploadAttachment(t *testing.T, srv *httptest.Server, filename, contentType string, content []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/attachments", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Bnbloc-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")

// Active formats are refused, and a declared type the content does not match
// (HTML smuggled as image/png or text/plain) is refused: an upload can never
// become script served from the app origin.
func TestUploadRejectsActiveAndMismatchedContent(t *testing.T) {
	srv, _ := newAttachmentServer(t)
	html := []byte("<html><script>alert(document.cookie)</script></html>")
	cases := []struct {
		name, ct string
		body     []byte
	}{
		{"x.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"x.html", "text/html", html},
		{"x.png", "image/png", html},
		{"x.txt", "text/plain", html},
		{"x.png", "image/png", []byte("GIF89a......")},
	}
	for _, c := range cases {
		if resp := uploadAttachment(t, srv, c.name, c.ct, c.body); resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("%s as %s: status %d, want 415", c.name, c.ct, resp.StatusCode)
		}
	}
}

func download(t *testing.T, srv *httptest.Server, id string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/attachments/"+id, nil)
	req.Header.Set("X-Bnbloc-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// A verified raster image renders inline; every response is nosniff and
// sandboxed by CSP.
func TestDownloadImageInlineSandboxed(t *testing.T) {
	srv, _ := newAttachmentServer(t)
	resp := uploadAttachment(t, srv, "pic.png", "image/png", pngBytes)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d", resp.StatusCode)
	}
	var meta struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&meta)
	got := download(t, srv, meta.ID)
	if ct := got.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if cd := got.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") {
		t.Errorf("Content-Disposition = %q, want inline", cd)
	}
	if got.Header.Get("X-Content-Type-Options") != "nosniff" || got.Header.Get("Content-Security-Policy") != attachmentCSP {
		t.Errorf("missing nosniff/CSP: %v", got.Header)
	}
}

// A stored row of an active type (e.g. an SVG uploaded before sniffing) is
// served as an opaque download, never with its renderable type.
func TestDownloadNonImageIsOpaqueAttachment(t *testing.T) {
	srv, svc := newAttachmentServer(t)
	key, _, _, _ := svc.Blobs.Put(context.Background(), strings.NewReader("<svg><script>alert(1)</script></svg>"))
	if err := svc.Attachments.PutAttachment(dctx(), store.Attachment{
		ID: "att_svg", RoomID: "room-1", Uploader: "alice", Filename: `evil".svg`,
		ContentType: "image/svg+xml", BlobKey: key,
	}); err != nil {
		t.Fatal(err)
	}
	got := download(t, srv, "att_svg")
	if ct := got.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	cd := got.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want attachment", cd)
	}
	if got.Header.Get("Content-Security-Policy") != attachmentCSP {
		t.Error("missing sandbox CSP")
	}
}

// Downloads declare their length, so a reader (and every proxy in between)
// can tell a cut transfer from a complete one.
func TestAttachmentDownloadDeclaresContentLength(t *testing.T) {
	srv, _ := newAttachmentServer(t)
	resp := uploadAttachment(t, srv, "pic.png", "image/png", pngBytes)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d", resp.StatusCode)
	}
	var meta struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&meta)
	dl := download(t, srv, meta.ID)
	if dl.ContentLength != int64(len(pngBytes)) {
		t.Fatalf("Content-Length = %d, want %d", dl.ContentLength, len(pngBytes))
	}
}
