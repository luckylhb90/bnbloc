package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// /v1/artifacts/{key}?direct=1 answers 302 to a presigned URL once the
// entitlement passed and realtime hands URLs out; without ?direct=1, or
// with direct downloads off, the bytes stream as before.
func TestArtifactDownload_direct_redirects_to_presigned_url(t *testing.T) {
	e := newSiteEnv(t)
	key, _, _, err := e.blobs.Put(context.Background(), bytes.NewReader([]byte("sidecar-binary")))
	if err != nil {
		t.Fatal(err)
	}
	admin := e.svc.Signer.Issue(auth.Claims{Subject: "root", Role: string(rbac.RoleSuperAdmin), ExpiresAtUnix: 1 << 40})
	member := e.session("alice")

	if resp, body := e.do(http.MethodGet, "/v1/artifacts/"+key+"?direct=1", admin); resp.StatusCode != http.StatusOK || body != "sidecar-binary" {
		t.Fatalf("direct off: %d %q", resp.StatusCode, body)
	}
	e.rt.Blobs = presigningDisk{e.blobs}
	e.rt.DirectDownloads = true
	resp, _ := e.do(http.MethodGet, "/v1/artifacts/"+key+"?direct=1", admin)
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "https://s3.example.com/bucket/"+key) {
		t.Fatalf("direct on: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := e.do(http.MethodGet, "/v1/artifacts/"+key, admin); resp.StatusCode != http.StatusOK || body != "sidecar-binary" ||
		resp.Header.Get("Content-Length") != "14" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("plain download: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	// The entitlement check runs first: a member not entitled to the key
	// gets 404, never a presigned URL.
	if resp, _ := e.do(http.MethodGet, "/v1/artifacts/"+key+"?direct=1", member); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unentitled member: %d", resp.StatusCode)
	}
}

// A deliverable read never takes the session from the URL, download
// navigation or not: the bearer rides the Authorization header.
func TestArtifactDownload_refuses_access_token(t *testing.T) {
	room := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "bytes")
	}))
	defer room.Close()
	svc := newGateway(t, room.URL)
	gw := httptest.NewServer(svc.Handler())
	defer gw.Close()
	token := svc.Signer.Issue(auth.Claims{Subject: "alice", Rooms: []string{"room-1"}, Tenant: "default", ExpiresAtUnix: 1 << 40})
	path := gw.URL + "/v1/rooms/room-1/artifacts/art_1/versions/2/content"
	for _, q := range []string{"?download=1&access_token=", "?access_token="} {
		resp, err := http.Get(path + q + token)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s<token>: %d, want 401", q, resp.StatusCode)
		}
	}
}
