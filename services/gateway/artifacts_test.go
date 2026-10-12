package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// artifactGateway wires a real realtime blob face (backed by an in-memory disk
// blob store in a temp dir) behind a gateway whose Backends.Realtime points at
// it, so the artifact upload/download exercise the true gateway->realtime
// svcauth-signed proxy path end to end.
func artifactGateway(t *testing.T, role rbac.Role) (*httptest.Server, string) {
	t.Helper()
	blobs, err := blobstore.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("disk store: %v", err)
	}
	rt := &realtimehttp.Service{Blobs: blobs}
	rtSrv := httptest.NewServer(svcauth.Middleware(svcauth.KeyFromEnv(), rt.Handler()))
	t.Cleanup(rtSrv.Close)

	svc := newGateway(t, "")
	svc.Backends.Realtime = rtSrv.URL
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", string(role), nil }
	gwSrv := httptest.NewServer(svc.Handler())
	t.Cleanup(gwSrv.Close)
	token, _ := login(t, gwSrv, "good")
	return gwSrv, token
}

func TestArtifactUploadDownloadRoundTrip(t *testing.T) {
	gw, token := artifactGateway(t, rbac.RoleSuperAdmin)
	payload := []byte("test-artifact-binary-\x00\x01\x02-bytes")

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/platform/artifacts", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("upload status = %d, body=%s", resp.StatusCode, body)
	}
	var put struct {
		Key    string `json:"key"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&put); err != nil {
		t.Fatalf("decode put result: %v", err)
	}
	resp.Body.Close()

	wantKey, wantSHA := blobstore.ContentKey(payload)
	if put.SHA256 != wantSHA {
		t.Fatalf("sha256 = %s, want %s", put.SHA256, wantSHA)
	}
	if put.Key != wantKey {
		t.Fatalf("key = %s, want %s", put.Key, wantKey)
	}
	if put.Size != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", put.Size, len(payload))
	}

	dl, _ := http.NewRequest(http.MethodGet, gw.URL+"/v1/artifacts/"+put.Key, nil)
	dl.Header.Set("Authorization", "Bearer "+token)
	dlResp, err := http.DefaultClient.Do(dl)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d", dlResp.StatusCode)
	}
	if dlResp.ContentLength != int64(len(payload)) {
		t.Fatalf("download Content-Length = %d, want %d", dlResp.ContentLength, len(payload))
	}
	got, _ := io.ReadAll(dlResp.Body)
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded bytes differ: got %d bytes, want %d", len(got), len(payload))
	}
}

// A download whose upstream read breaks mid-body must reach the client as a
// broken transfer (declared length, then an error), never as a short 200 that
// looks complete — the production failure that truncated desktop updates.
func TestArtifactDownloadForwardsLengthAndAbortsOnUpstreamCut(t *testing.T) {
	const declared = 1 << 20
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(declared))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte{7}, declared/2))
		w.(http.Flusher).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	t.Cleanup(upstream.Close)

	svc := newGateway(t, "")
	svc.Backends.Realtime = upstream.URL
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", string(rbac.RoleSuperAdmin), nil }
	gw := httptest.NewServer(svc.Handler())
	t.Cleanup(gw.Close)
	token, _ := login(t, gw, "good")

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/v1/artifacts/sha256/"+strings.Repeat("a", 64), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.ContentLength != declared {
		t.Fatalf("Content-Length = %d, want %d forwarded", resp.ContentLength, declared)
	}
	got, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("read %d of %d bytes with no error: a cut transfer looked complete", len(got), declared)
	}
}

func TestArtifactUploadRejectsNonSuperAdmin(t *testing.T) {
	gw, token := artifactGateway(t, rbac.RoleAdmin)
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/platform/artifacts", bytes.NewReader([]byte("x")))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestArtifactDownloadMissingKey404(t *testing.T) {
	gw, token := artifactGateway(t, rbac.RoleSuperAdmin)
	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/v1/artifacts/sha256/"+missingHex, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

const missingHex = "0000000000000000000000000000000000000000000000000000000000000000"
