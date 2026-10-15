package room

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
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/attachment"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// memoryBlobs is an in-memory ArtifactBlobs.
type memoryBlobs struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (b *memoryBlobs) PutBlob(_ context.Context, r io.Reader) (realtime.BlobRef, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return realtime.BlobRef{}, err
	}
	sum := sha256.Sum256(raw)
	key := "sha256/" + hex.EncodeToString(sum[:])
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string][]byte{}
	}
	b.m[key] = raw
	return realtime.BlobRef{Key: key, SHA256: key[7:], Size: int64(len(raw))}, nil
}

func (b *memoryBlobs) OpenBlob(_ context.Context, key string) (io.ReadCloser, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, ok := b.m[key]
	if !ok {
		return nil, 0, errors.New("no blob")
	}
	return io.NopCloser(bytes.NewReader(raw)), int64(len(raw)), nil
}

func (b *memoryBlobs) PresignBlob(context.Context, string, string, string) (realtime.BlobPresign, error) {
	return realtime.BlobPresign{}, realtime.ErrDirectDownloadsDisabled
}

var appPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")

// upload posts one file as p.
func (e *appEnv) upload(p who, name, contentType string, content []byte, public bool) reply {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = mw.Close()
	path := "/internal/v1/app/files"
	if public {
		path += "?public=1"
	}
	k := svcauth.Key(appTestKey)
	req, err := k.NewRequest(context.Background(), http.MethodPost, e.srv.URL+path, &body, svcauth.ServiceSubject("gateway"), e.tenant)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if err := k.AssertApp(req, svcauth.AppScope{RoomID: e.room, AppID: e.app, Kind: p.kind, Participant: p.id}); err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: out}
}

func (r reply) file() wire.AppFile {
	var f wire.AppFile
	_ = json.Unmarshal(r.body, &f)
	return f
}

func newAppFileEnv(t *testing.T, b *store.Backends) *appEnv {
	e := newAppEnv(t, b)
	e.svc.Blobs = &memoryBlobs{}
	return e
}

// A page uploads a file checked like a chat attachment; its uploader and
// the owners read it, others only when it is public; it is served as an
// attachment is; only its uploader or an owner deletes it.
func TestAppFilesUploadReadDelete(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppFileEnv(t, b)
		r := e.upload(visitA, "receipt.png", "image/png", appPNG, false)
		if r.status != http.StatusCreated {
			t.Fatalf("upload = %d %s", r.status, r.body)
		}
		f := r.file()
		if f.ID == "" || f.Name != "receipt.png" || f.Type != "image/png" || f.Size != int64(len(appPNG)) || f.By != e.me(visitA) || f.Author != "" {
			t.Fatalf("uploaded file = %+v", f)
		}
		if got := e.do(maya, http.MethodGet, "/files/"+f.ID, nil).file(); got.Author != "visitor:v-a" || got.Name != "receipt.png" {
			t.Fatalf("an owner reads %+v; want the real uploader", got)
		}
		if r := e.do(visitB, http.MethodGet, "/files/"+f.ID, nil); r.status != http.StatusNotFound {
			t.Fatalf("another visitor reads a private file: %d", r.status)
		}
		c := e.do(visitA, http.MethodGet, "/files/"+f.ID+"/content", nil)
		if c.status != http.StatusOK || !bytes.Equal(c.body, appPNG) {
			t.Fatalf("content = %d %q", c.status, c.body)
		}

		pub := e.upload(visitA, "notes.txt", "text/plain", []byte("hello"), true).file()
		if r := e.do(visitB, http.MethodGet, "/files/"+pub.ID, nil); r.status != http.StatusOK {
			t.Fatalf("a public file is readable: %d", r.status)
		}
		if r := e.do(visitB, http.MethodDelete, "/files/"+pub.ID, nil); r.status != http.StatusForbidden {
			t.Fatalf("another visitor deletes a public file: %d", r.status)
		}
		if r := e.do(visitB, http.MethodDelete, "/files/"+f.ID, nil); r.status != http.StatusNotFound {
			t.Fatalf("another visitor deletes a private file: %d", r.status)
		}
		if r := e.do(maya, http.MethodDelete, "/files/"+pub.ID, nil); r.status != http.StatusNoContent {
			t.Fatalf("an owner deletes: %d %s", r.status, r.body)
		}
		if r := e.do(visitA, http.MethodDelete, "/files/"+f.ID, nil); r.status != http.StatusNoContent {
			t.Fatalf("the uploader deletes: %d %s", r.status, r.body)
		}
		if r := e.do(visitA, http.MethodGet, "/files/"+f.ID, nil); r.status != http.StatusNotFound {
			t.Fatalf("a deleted file reads: %d", r.status)
		}
	})
}

// What a chat attachment may not be, an app file may not be either; the
// size cap is the attachment cap; markup downloads opaque.
func TestAppFilesRefuseAndServeLikeAttachments(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppFileEnv(t, b)
		if r := e.upload(jack, "tool.exe", "application/octet-stream", []byte("MZ"), false); r.status != http.StatusUnsupportedMediaType || !strings.Contains(string(r.body), "tool.exe") {
			t.Fatalf("an .exe = %d %s", r.status, r.body)
		}
		if r := e.upload(jack, "fake.png", "image/png", []byte("<html><script>x</script>"), false); r.status != http.StatusUnsupportedMediaType {
			t.Fatalf("HTML named .png = %d %s", r.status, r.body)
		}
		big := append(bytes.Clone(appPNG), make([]byte, wire.MaxAttachmentBytes)...)
		if r := e.upload(jack, "big.png", "image/png", big, false); r.status != http.StatusRequestEntityTooLarge {
			t.Fatalf("a file over the cap = %d %s", r.status, r.body)
		}
		page := e.upload(jack, "page.html", "text/html", []byte("<html><script>alert(1)</script></html>"), false).file()
		k := svcauth.Key(appTestKey)
		req, _ := k.NewRequest(context.Background(), http.MethodGet, e.srv.URL+"/internal/v1/app/files/"+page.ID+"/content", nil, svcauth.ServiceSubject("gateway"), e.tenant)
		_ = k.AssertApp(req, svcauth.AppScope{RoomID: e.room, AppID: e.app, Kind: jack.kind, Participant: jack.id})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") ||
			resp.Header.Get("Content-Security-Policy") != attachment.CSP || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("an HTML file is served %v", resp.Header)
		}
	})
}

// Files count in the owners' stats, are capped per app, and a wipe deletes
// them with the documents.
func TestAppFilesCapsStatsAndWipe(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppFileEnv(t, b)
		e.svc.appCaps = appCaps{docs: 10, bytes: 1 << 20, collections: 4, files: 2, fileBytes: 1 << 20}
		ids := []string{}
		for i := 0; i < 2; i++ {
			ids = append(ids, e.upload(jack, "a.png", "image/png", appPNG, false).file().ID)
		}
		if r := e.upload(jack, "a.png", "image/png", appPNG, false); r.status != http.StatusConflict {
			t.Fatalf("a file over the app's file cap = %d %s", r.status, r.body)
		}
		var st wire.AppDataStats
		_ = json.Unmarshal(e.do(maya, http.MethodGet, "/stats", nil).body, &st)
		if st.Files != 2 || st.FileBytes != int64(2*len(appPNG)) {
			t.Fatalf("stats = %+v", st)
		}
		if r := e.do(maya, http.MethodDelete, "/data", nil); r.status != http.StatusOK {
			t.Fatalf("wipe = %d %s", r.status, r.body)
		}
		for _, id := range ids {
			if r := e.do(maya, http.MethodGet, "/files/"+id, nil); r.status != http.StatusNotFound {
				t.Fatalf("a file outlived the wipe: %d", r.status)
			}
		}
	})
}
