package room

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// filesRoom creates room "files" owned by alice with member bob (carol is
// not a member) in a handoff env: the real room + realtime services, with
// blob versions stored in realtime's blob store over the signed client.
func filesRoom(t *testing.T) *handoffEnv {
	t.Helper()
	e := newHandoffEnv(t)
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms", "alice", map[string]any{"roomId": "files", "name": "Files"})
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms/files/members", "alice", map[string]any{"userId": "bob", "role": "member"})
	return e
}

func (e *handoffEnv) createArtifact(user string, body map[string]any) store.Artifact {
	e.t.Helper()
	var out struct {
		Artifact store.Artifact `json:"artifact"`
	}
	if err := json.Unmarshal(e.must(e.roomSrv, http.MethodPost, "/v1/rooms/files/artifacts", user, body), &out); err != nil {
		e.t.Fatal(err)
	}
	return out.Artifact
}

type versionReply struct {
	Artifact  store.Artifact        `json:"artifact"`
	Version   store.ArtifactVersion `json:"version"`
	Unchanged bool                  `json:"unchanged"`
	Stored    bool                  `json:"stored"`
	Error     string                `json:"error"`
}

// postFile publishes a blob version through the multipart form.
func (e *handoffEnv) postFile(user, artifactID string, fields map[string]string, fileName, contentType string, content []byte) (int, versionReply) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, fileName))
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(content)
	// Fields after the file: the order must not matter.
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, e.roomSrv.URL+"/v1/rooms/files/artifacts/"+artifactID+"/versions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Bnbloc-User", user)
	return e.send(req)
}

func (e *handoffEnv) send(req *http.Request) (int, versionReply) {
	e.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out versionReply
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *handoffEnv) get(path, user string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.roomSrv.URL+path, nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func (e *handoffEnv) putThumb(user, artifactID string, version int, source, contentType string, body []byte) (int, versionReply) {
	e.t.Helper()
	path := fmt.Sprintf("%s/v1/rooms/files/artifacts/%s/versions/%d/thumbnail", e.roomSrv.URL, artifactID, version)
	if source != "" {
		path += "?source=" + source
	}
	req, _ := http.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Bnbloc-User", user)
	return e.send(req)
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A blob version publishes through multipart, lands in the blob store with
// its MIME type, size, hash and source link, and serves back byte-exact with
// the safe headers; membership, the cap, the raster-type check, CAS, and the
// identical-bytes dedupe all hold.
func TestBlobVersionPublishAndContent(t *testing.T) {
	e := filesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "image", "title": "assets/chart.png", "fileName": "chart.png"})
	img := encodePNG(t, 8, 8)

	code, rep := e.postFile("alice", art.ArtifactID, map[string]string{"baseVersion": "0", "summary": "first", "sourceJobId": "job-1", "sourceSeq": "7"}, "chart.png", "image/png", img)
	if code != http.StatusOK {
		t.Fatalf("publish = %d %s", code, rep.Error)
	}
	v := rep.Version
	if v.Version != 1 || v.Storage != store.ArtifactStorageBlob || len(v.ContentHash) != 64 ||
		v.SizeBytes != int64(len(img)) || v.MimeType != "image/png" || v.SourceJobID != "job-1" || v.SourceSeq != 7 || v.Content != "" {
		t.Fatalf("blob version = %+v", v)
	}
	if rep.Artifact.MimeType != "image/png" || rep.Artifact.FileName != "chart.png" {
		t.Fatalf("artifact = %+v", rep.Artifact)
	}
	// The blob key and the tenant stay server-side (decision O4): members
	// read the bytes through the content route.
	if v.BlobKey != "" || v.TenantID != "" || rep.Artifact.TenantID != "" {
		t.Fatalf("a member saw server-side fields: version %+v, artifact %+v", v, rep.Artifact)
	}
	if stored, err := e.svc.Artifacts.GetArtifactVersion(dctx(), art.ArtifactID, 1); err != nil || stored.BlobKey != "sha256/"+v.ContentHash {
		t.Fatalf("stored version = %+v, %v", stored, err)
	}

	resp := e.get("/v1/rooms/files/artifacts/"+art.ArtifactID+"/versions/1/content", "bob")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, img) {
		t.Fatalf("content = %d (%d bytes)", resp.StatusCode, len(body))
	}
	h := resp.Header
	if h.Get("Content-Type") != "image/png" || !strings.HasPrefix(h.Get("Content-Disposition"), "inline") ||
		!strings.Contains(h.Get("Content-Disposition"), "chart.png") || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Security-Policy") != "default-src 'none'; sandbox" || h.Get("Content-Length") != fmt.Sprint(len(img)) ||
		h.Get("ETag") != `"`+v.ContentHash+`"` {
		t.Fatalf("content headers = %v", h)
	}
	if resp := e.get("/v1/rooms/files/artifacts/"+art.ArtifactID+"/versions/1/content", "carol"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member content = %d, want 403", resp.StatusCode)
	}

	// Same bytes again: no new revision.
	code, rep = e.postFile("bob", art.ArtifactID, map[string]string{"baseVersion": "1"}, "chart.png", "image/png", img)
	if code != http.StatusOK || !rep.Unchanged || rep.Version.Version != 1 {
		t.Fatalf("dedupe = %d %+v", code, rep)
	}
	// A declared raster type must match the bytes.
	if code, _ := e.postFile("bob", art.ArtifactID, map[string]string{"baseVersion": "1"}, "chart.png", "image/png", []byte("<html><script>x</script>")); code != http.StatusUnsupportedMediaType {
		t.Fatalf("mismatched image = %d, want 415", code)
	}
	// CAS: a stale base is a conflict.
	if code, _ := e.postFile("bob", art.ArtifactID, map[string]string{"baseVersion": "0"}, "chart.png", "image/png", encodePNG(t, 9, 9)); code != http.StatusConflict {
		t.Fatalf("stale base = %d, want 409", code)
	}
	if code, _ := e.postFile("bob", art.ArtifactID, nil, "chart.png", "image/png", encodePNG(t, 9, 9)); code != http.StatusBadRequest {
		t.Fatalf("missing baseVersion = %d, want 400", code)
	}
	if code, _ := e.postFile("carol", art.ArtifactID, map[string]string{"baseVersion": "1"}, "chart.png", "image/png", img); code != http.StatusForbidden {
		t.Fatalf("non-member publish = %d, want 403", code)
	}
	e.svc.ArtifactMaxBytes = 64
	if code, _ := e.postFile("bob", art.ArtifactID, map[string]string{"baseVersion": "1"}, "chart.png", "image/png", encodePNG(t, 64, 64)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over cap = %d, want 413", code)
	}
	e.svc.ArtifactMaxBytes = 0

	// A generic file: typed by extension when the part declares no type, and
	// served as an attachment.
	doc := e.createArtifact("bob", map[string]any{"kind": "file", "title": "report.pdf", "fileName": "report.pdf"})
	pdf := []byte("%PDF-1.4\n%binary\x00\x01\n")
	code, rep = e.postFile("bob", doc.ArtifactID, map[string]string{"baseVersion": "0"}, "report.pdf", "", pdf)
	if code != http.StatusOK || rep.Version.MimeType != "application/pdf" {
		t.Fatalf("pdf publish = %d %+v", code, rep)
	}
	resp = e.get("/v1/rooms/files/artifacts/"+doc.ArtifactID+"/versions/1/content", "alice")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || !bytes.Equal(body, pdf) {
		t.Fatalf("pdf content headers = %v", resp.Header)
	}
}

// Inline (text) versions serve through the same content route, typed from
// the artifact, and carry the JSON publish's source link.
func TestInlineVersionContentAndSource(t *testing.T) {
	e := filesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Spec", "fileName": "spec.md", "content": "# v1"})
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms/files/artifacts/"+art.ArtifactID+"/versions", "bob",
		map[string]any{"baseVersion": 1, "content": "# v2", "sourceJobId": "job-2", "sourceSeq": 12})
	var got struct {
		Version store.ArtifactVersion `json:"version"`
	}
	_ = json.Unmarshal(e.must(e.roomSrv, http.MethodGet, "/v1/rooms/files/artifacts/"+art.ArtifactID+"/versions/2", "alice", nil), &got)
	if got.Version.Storage != store.ArtifactStorageInline || got.Version.SourceSeq != 12 || got.Version.SourceJobID != "job-2" ||
		got.Version.MimeType != "text/markdown" || got.Version.SizeBytes != 4 {
		t.Fatalf("inline version = %+v", got.Version)
	}
	resp := e.get("/v1/rooms/files/artifacts/"+art.ArtifactID+"/versions/2/content", "bob")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "# v2" || resp.Header.Get("Content-Type") != "text/markdown; charset=utf-8" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("inline content = %q %v", body, resp.Header)
	}
	if code, _ := e.do(e.roomSrv, http.MethodPost, "/v1/rooms/files/artifacts/"+art.ArtifactID+"/versions", "bob",
		map[string]any{"baseVersion": 2, "content": "x", "sourceSeq": -1}); code != http.StatusBadRequest {
		t.Fatalf("negative sourceSeq = %d, want 400", code)
	}
}

// Thumbnails: PNG/WebP only, verified by magic + header dimensions, bounded
// in bytes and pixels; first write wins unless a higher-ranked source
// replaces it.
func TestArtifactThumbnails(t *testing.T) {
	e := filesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "file", "title": "deck.pptx", "fileName": "deck.pptx"})
	if code, rep := e.postFile("alice", art.ArtifactID, map[string]string{"baseVersion": "0"}, "deck.pptx", "", []byte("PK\x03\x04deck")); code != http.StatusOK {
		t.Fatalf("publish = %d %s", code, rep.Error)
	}
	if resp := e.get("/v1/rooms/files/artifacts/"+art.ArtifactID+"/versions/1/thumbnail", "bob"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no thumbnail = %d, want 404", resp.StatusCode)
	}
	thumb := encodePNG(t, 64, 48)
	code, rep := e.putThumb("bob", art.ArtifactID, 1, "", "image/png", thumb)
	if code != http.StatusOK || !rep.Stored || rep.Version.Thumbnail == nil || *rep.Version.Thumbnail != (store.ArtifactThumbnail{MimeType: "image/png", Width: 64, Height: 48}) {
		t.Fatalf("thumbnail put = %d %+v", code, rep)
	}
	resp := e.get("/v1/rooms/files/artifacts/"+art.ArtifactID+"/versions/1/thumbnail", "alice")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, thumb) || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("Content-Disposition") != "inline" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("thumbnail get = %d %v", resp.StatusCode, resp.Header)
	}
	// Same-rank later write is ignored; a previewer's replaces the builtin.
	if code, rep := e.putThumb("alice", art.ArtifactID, 1, "builtin", "image/png", encodePNG(t, 32, 32)); code != http.StatusOK || rep.Stored || rep.Version.Thumbnail.Width != 64 {
		t.Fatalf("second builtin = %d %+v", code, rep)
	}
	if code, rep := e.putThumb("alice", art.ArtifactID, 1, "previewer", "image/webp", webpVP8L(100, 50)); code != http.StatusOK || !rep.Stored ||
		*rep.Version.Thumbnail != (store.ArtifactThumbnail{MimeType: "image/webp", Width: 100, Height: 50}) {
		t.Fatalf("previewer thumbnail = %d %+v", code, rep)
	}
	for name, c := range map[string]struct {
		source, ct string
		body       []byte
		want       int
	}{
		"not a png":     {"previewer", "image/png", []byte("<svg onload=x>"), http.StatusUnsupportedMediaType},
		"jpeg":          {"previewer", "image/jpeg", thumb, http.StatusUnsupportedMediaType},
		"too wide":      {"previewer", "image/png", encodePNG(t, 1025, 4), http.StatusBadRequest},
		"too many byte": {"previewer", "image/png", append(append([]byte{}, thumb...), make([]byte, maxThumbnailBytes)...), http.StatusRequestEntityTooLarge},
		"bad source":    {"bogus", "image/png", thumb, http.StatusBadRequest},
	} {
		if code, _ := e.putThumb("bob", art.ArtifactID, 1, c.source, c.ct, c.body); code != c.want {
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
	}
	if code, _ := e.putThumb("carol", art.ArtifactID, 1, "", "image/png", thumb); code != http.StatusForbidden {
		t.Fatalf("non-member thumbnail = %d, want 403", code)
	}
	if code, _ := e.putThumb("bob", art.ArtifactID, 9, "", "image/png", thumb); code != http.StatusNotFound {
		t.Fatalf("missing version thumbnail = %d, want 404", code)
	}
}

// webpVP8L builds a minimal lossless WebP header with the given canvas.
func webpVP8L(w, h int) []byte {
	b := make([]byte, 30)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WEBPVP8L")
	binary.LittleEndian.PutUint32(b[16:], 10)
	b[20] = 0x2f
	binary.LittleEndian.PutUint32(b[21:], uint32(w-1)|uint32(h-1)<<14)
	return b
}

func TestWebpDimensions(t *testing.T) {
	if w, h, err := webpDimensions(webpVP8L(1024, 3)); err != nil || w != 1024 || h != 3 {
		t.Fatalf("VP8L = %d %d %v", w, h, err)
	}
	x := webpVP8L(1, 1)
	copy(x[12:], "VP8X")
	x[24], x[25], x[26] = 0xff, 0x01, 0 // 511+1
	x[27], x[28], x[29] = 0x09, 0, 0    // 9+1
	if w, h, err := webpDimensions(x); err != nil || w != 512 || h != 10 {
		t.Fatalf("VP8X = %d %d %v", w, h, err)
	}
	l := webpVP8L(1, 1)
	copy(l[12:], "VP8 ")
	l[23], l[24], l[25] = 0x9d, 0x01, 0x2a
	binary.LittleEndian.PutUint16(l[26:], 320)
	binary.LittleEndian.PutUint16(l[28:], 200)
	if w, h, err := webpDimensions(l); err != nil || w != 320 || h != 200 {
		t.Fatalf("VP8 = %d %d %v", w, h, err)
	}
	if _, _, err := webpDimensions([]byte("RIFF....WEBPjunk")); err == nil {
		t.Fatal("short webp accepted")
	}
}

// The list filters (kind, mime incl. "type/*", author, sourceSeq, q) and
// keyset paging, plus the blob cap publishers read.
func TestListArtifactFiltersAndPaging(t *testing.T) {
	e := filesRoom(t)
	md := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Spec", "fileName": "spec.md", "content": "# s"})
	img := e.createArtifact("bob", map[string]any{"kind": "image", "title": "assets/chart.png", "fileName": "chart.png"})
	if code, rep := e.postFile("bob", img.ArtifactID, map[string]string{"baseVersion": "0", "sourceSeq": "7"}, "chart.png", "image/png", encodePNG(t, 2, 2)); code != http.StatusOK {
		t.Fatalf("publish = %d %s", code, rep.Error)
	}
	list := func(q string) ([]store.Artifact, string, int64) {
		var out struct {
			Artifacts  []store.Artifact `json:"artifacts"`
			NextCursor string           `json:"nextCursor"`
			MaxBytes   int64            `json:"maxBytes"`
		}
		_ = json.Unmarshal(e.must(e.roomSrv, http.MethodGet, "/v1/rooms/files/artifacts"+q, "alice", nil), &out)
		return out.Artifacts, out.NextCursor, out.MaxBytes
	}
	ids := func(arts []store.Artifact) []string {
		out := []string{}
		for _, a := range arts {
			out = append(out, a.ArtifactID)
		}
		return out
	}
	if arts, _, maxBytes := list(""); len(arts) != 2 || maxBytes != DefaultArtifactMaxBytes {
		t.Fatalf("all = %v maxBytes=%d", ids(arts), maxBytes)
	}
	for q, want := range map[string]string{
		"?kind=image": img.ArtifactID, "?mime=image/*": img.ArtifactID, "?mime=image/png": img.ArtifactID,
		"?author=alice": md.ArtifactID, "?sourceSeq=7": img.ArtifactID, "?q=CHART": img.ArtifactID, "?q=spec.md": md.ArtifactID,
	} {
		if arts, _, _ := list(q); len(arts) != 1 || arts[0].ArtifactID != want {
			t.Errorf("list%s = %v, want [%s]", q, ids(arts), want)
		}
	}
	first, cursor, _ := list("?limit=1")
	if len(first) != 1 || cursor == "" {
		t.Fatalf("page 1 = %v cursor=%q", ids(first), cursor)
	}
	second, cursor2, _ := list("?limit=1&cursor=" + cursor)
	if len(second) != 1 || second[0].ArtifactID == first[0].ArtifactID {
		t.Fatalf("page 2 = %v after %v", ids(second), ids(first))
	}
	if third, _, _ := list("?limit=1&cursor=" + cursor2); len(third) != 0 {
		t.Fatalf("page 3 = %v, want empty", ids(third))
	}
	for _, bad := range []string{"?limit=0", "?limit=500", "?sourceSeq=x", "?cursor=nope"} {
		if code, _ := e.do(e.roomSrv, http.MethodGet, "/v1/rooms/files/artifacts"+bad, "alice", nil); code != http.StatusBadRequest {
			t.Errorf("list%s = %d, want 400", bad, code)
		}
	}
}

// A room handoff carries blob versions as files: the export holds the raw
// bytes and their typed metadata; the import re-stores them in the blob
// store and the imported room serves them byte-exact (source links stay
// behind: they name the source room's jobs and messages).
func TestHandoffRoundTripBinaryArtifact(t *testing.T) {
	e := filesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "image", "title": "chart", "fileName": "chart.png"})
	v1, v2 := encodePNG(t, 3, 3), encodePNG(t, 4, 4)
	for i, b := range [][]byte{v1, v2} {
		if code, rep := e.postFile("alice", art.ArtifactID, map[string]string{"baseVersion": fmt.Sprint(i), "sourceSeq": "5"}, "chart.png", "image/png", b); code != http.StatusOK {
			t.Fatalf("publish v%d = %d %s", i+1, code, rep.Error)
		}
	}
	files := readZip(t, e.must(e.roomSrv, http.MethodGet, "/v1/rooms/files/export", "alice", nil))
	dir := "artifacts/" + packageSegment(art.ArtifactID) + "/"
	if !bytes.Equal(files[dir+"v1.png"], v1) || !bytes.Equal(files[dir+"v2.png"], v2) {
		t.Fatalf("export lacks the blob bytes: %v", keys(files))
	}
	var meta wire.RoomExportArtifact
	if err := json.Unmarshal(files[dir+"artifact.json"], &meta); err != nil {
		t.Fatal(err)
	}
	if meta.FileName != "chart.png" || meta.MimeType != "image/png" || len(meta.Versions) != 2 ||
		meta.Versions[1].Storage != store.ArtifactStorageBlob || meta.Versions[1].SizeBytes != int64(len(v2)) || meta.Versions[1].SourceSeq != 5 {
		t.Fatalf("artifact.json = %+v", meta)
	}

	code, out := e.importPackage("dave", writeZip(t, files))
	if code != http.StatusOK {
		t.Fatalf("import = %d %s", code, out)
	}
	var res struct {
		RoomID string `json:"roomId"`
	}
	_ = json.Unmarshal(out, &res)
	var arts struct {
		Artifacts []store.Artifact `json:"artifacts"`
	}
	_ = json.Unmarshal(e.must(e.roomSrv, http.MethodGet, "/v1/rooms/"+res.RoomID+"/artifacts", "dave", nil), &arts)
	if len(arts.Artifacts) != 1 || arts.Artifacts[0].FileName != "chart.png" || arts.Artifacts[0].MimeType != "image/png" || arts.Artifacts[0].CurrentVersion != 2 {
		t.Fatalf("imported = %+v", arts.Artifacts)
	}
	id := arts.Artifacts[0].ArtifactID
	for n, want := range map[int][]byte{1: v1, 2: v2} {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/rooms/%s/artifacts/%s/versions/%d/content", e.roomSrv.URL, res.RoomID, id, n), nil)
		req.Header.Set("X-Bnbloc-User", "dave")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !bytes.Equal(body, want) || resp.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("imported v%d content = %d", n, resp.StatusCode)
		}
	}
	var ver struct {
		Version store.ArtifactVersion `json:"version"`
	}
	_ = json.Unmarshal(e.must(e.roomSrv, http.MethodGet, "/v1/rooms/"+res.RoomID+"/artifacts/"+id+"/versions/2", "dave", nil), &ver)
	if ver.Version.Storage != store.ArtifactStorageBlob || ver.Version.SourceSeq != 0 || ver.Version.MimeType != "image/png" {
		t.Fatalf("imported version = %+v", ver.Version)
	}

	// A tampered blob file fails validation before anything is created.
	files[dir+"v2.png"] = encodePNG(t, 5, 5)
	if code, _ := e.importPackage("dave", writeZip(t, files)); code != http.StatusBadRequest {
		t.Fatalf("tampered blob import = %d, want 400", code)
	}
}

func keys(m map[string][]byte) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
