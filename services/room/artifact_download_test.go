package room

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/blobstore"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

// presigningBlobs makes realtime's blob store presign (a stand-in for S3
// with direct downloads on).
type presigningBlobs struct{ realtimehttp.BlobStore }

func (presigningBlobs) PresignGet(_ context.Context, key string, _ time.Duration, opts blobstore.PresignOptions) (string, error) {
	return "https://s3.example.com/b/" + key + "?ct=" + opts.ContentType, nil
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (e *handoffEnv) getNoRedirect(path, user string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.roomSrv.URL+path, nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := noRedirect.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

// ?download=1 on a blob version: a 302 to a presigned URL when realtime
// hands them out, else the bytes as an attachment (even an inline-safe
// image); plain viewer reads never redirect.
func TestArtifactContentDownload(t *testing.T) {
	e := filesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "image", "title": "chart.png", "fileName": "chart.png"})
	img := encodePNG(t, 4, 4)
	if code, rep := e.postFile("alice", art.ArtifactID, map[string]string{"baseVersion": "0"}, "chart.png", "image/png", img); code != http.StatusOK {
		t.Fatalf("publish = %d %s", code, rep.Error)
	}
	path := "/v1/rooms/files/artifacts/" + art.ArtifactID + "/versions/1/content"

	resp := e.getNoRedirect(path+"?download=1", "bob")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) != len(img) || resp.Header.Get("Content-Disposition") != "attachment; filename=chart.png" {
		t.Fatalf("direct off: %d %q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}

	e.rt.Blobs = presigningBlobs{e.rt.Blobs}
	e.rt.DirectDownloads = true
	resp = e.getNoRedirect(path+"?download=1", "bob")
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "https://s3.example.com/b/sha256/") || !strings.HasSuffix(loc, "?ct=image/png") ||
		resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("direct on: %d %q %v", resp.StatusCode, loc, resp.Header)
	}
	if resp = e.getNoRedirect(path, "bob"); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer read = %d, want 200 (no redirect without ?download=1)", resp.StatusCode)
	}
	resp.Body.Close()
	if resp = e.getNoRedirect(path+"?download=1", "carol"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member download = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}
