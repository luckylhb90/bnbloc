package gateway

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

func TestComponentsInstall_whenSignedEndpointConforms(t *testing.T) {
	// Given
	gw, srv, token, key := signedPackageGateway(t, true)
	t.Cleanup(func() { _ = core.Unregister("acme-agents") })
	coreSrv := conformingCoreServer(t, true)
	body := endpointPackageBody(t, coreSrv.URL, "1.0", "1.0")

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", status, respBody)
	}
	if gw.coreByName("acme-agents") == nil {
		t.Fatal("installed endpoint core was not registered without restart")
	}
	pkgs := listComponentsForAdmin(t, srv.URL, token)
	if len(pkgs) != 1 || pkgs[0].Name != "acme-agents" || pkgs[0].KeyID != key.id {
		t.Fatalf("packages = %+v, want installed acme-agents signed by trusted key", pkgs)
	}
	settings := gw.PlatformSettings
	if _, ok, err := settings.GetPlatformSetting(t.Context(), "ext.core.acme-agents.installed"); err != nil || !ok {
		t.Fatalf("extpipe record exists = %v, %v; want stored ext.core.acme-agents.installed", ok, err)
	}
	if _, ok, err := settings.GetPlatformSetting(t.Context(), "corepkg.acme-agents.installed"); err != nil || ok {
		t.Fatalf("legacy record exists = %v, %v; want extpipe persistence only", ok, err)
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/platform/components/acme-agents", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete core package: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d, want 204", resp.StatusCode)
	}
	if gw.coreByName("acme-agents") != nil {
		t.Fatal("deleted endpoint core remains active")
	}
	if _, ok, err := settings.GetPlatformSetting(t.Context(), "ext.core.acme-agents.installed"); err != nil || ok {
		t.Fatalf("extpipe record after delete exists = %v, %v; want deleted", ok, err)
	}
}

func TestComponentsInstall_refusesSignatureAndProtocolFailures(t *testing.T) {
	cases := []struct {
		name      string
		body      func(*testing.T, string) []byte
		signature func(testKey, []byte) string
		trusted   bool
		want      string
	}{
		{name: "unsigned", body: func(t *testing.T, u string) []byte { return endpointPackageBody(t, u, "1.0", "1.0") }, signature: func(testKey, []byte) string { return "" }, trusted: true, want: "signature required"},
		{name: "untrusted", body: func(t *testing.T, u string) []byte { return endpointPackageBody(t, u, "1.0", "1.0") }, signature: func(k testKey, b []byte) string { return signPackage(k.private, b) }, trusted: false, want: "untrusted publisher key"},
		{name: "tampered", body: func(t *testing.T, u string) []byte { return endpointPackageBody(t, u, "1.0", "1.0") }, signature: func(k testKey, b []byte) string { return signPackage(k.private, b) }, trusted: true, want: "signature verification failed"},
		{name: "protocol", body: func(t *testing.T, u string) []byte { return endpointPackageBody(t, u, "2.0", "2.0") }, signature: func(k testKey, b []byte) string { return signPackage(k.private, b) }, trusted: true, want: "bnbloc supports 1.0-1.0, core supports 2.0-2.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			_, srv, token, key := signedPackageGateway(t, tc.trusted)
			coreSrv := conformingCoreServer(t, true)
			body := tc.body(t, coreSrv.URL)
			sentBody := body
			if tc.name == "tampered" {
				sentBody = []byte(strings.Replace(string(body), "acme-agents", "other-agents", 1))
			}

			// When
			respBody, status := postComponent(t, srv.URL, token, sentBody, tc.signature(key, body))

			// Then
			if status != http.StatusBadRequest || !strings.Contains(string(respBody), tc.want) {
				t.Fatalf("status=%d body=%s, want 400 containing %q", status, respBody, tc.want)
			}
		})
	}
}

func TestComponentsInstall_refusesFailedConformanceProbe(t *testing.T) {
	// Given
	_, srv, token, key := signedPackageGateway(t, true)
	coreSrv := conformingCoreServer(t, false)
	body := endpointPackageBody(t, coreSrv.URL, "1.0", "1.0")

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusBadRequest || !strings.Contains(string(respBody), "conformance probe failed") {
		t.Fatalf("status=%d body=%s, want conformance refusal", status, respBody)
	}
}

func TestComponentsAdminEndpoints_requireSuperAdmin(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		t.Run(method+" forbidden", func(t *testing.T) {
			// Given
			svc := newGateway(t, "")
			svc.PlatformSettings = store.NewMemoryPlatformSettings()
			svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", string(rbac.RoleAdmin), nil }
			srv := httptest.NewServer(svc.Handler())
			defer srv.Close()
			token, _ := login(t, srv, "good")

			// When
			status := componentEndpointStatus(t, srv.URL, token, method)

			// Then
			if status != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", status)
			}
		})
		t.Run(method+" unauthenticated", func(t *testing.T) {
			// Given
			srv := httptest.NewServer(newGateway(t, "").Handler())
			defer srv.Close()

			// When
			status := componentEndpointStatus(t, srv.URL, "", method)

			// Then
			if status != http.StatusUnauthorized {
				t.Fatalf("status=%d, want 401", status)
			}
		})
	}
}

func TestComponentsAdminEndpoints_returnNotFoundForLegacyCorePackageRoutes(t *testing.T) {
	// Given
	svc := newGateway(t, "")
	svc.PlatformSettings = store.NewMemoryPlatformSettings()
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", string(rbac.RoleSuperAdmin), nil }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/v1/platform/core-packages"},
		{method: http.MethodPost, path: "/v1/platform/core-packages"},
		{method: http.MethodDelete, path: "/v1/platform/core-packages/acme-agents"},
		{method: http.MethodPut, path: "/v1/platform/core-packages/acme-agents/rollout"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			// When
			req, _ := http.NewRequest(tt.method, srv.URL+tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("legacy component route: %v", err)
			}
			defer resp.Body.Close()

			// Then
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status=%d, want 404", resp.StatusCode)
			}
		})
	}
}

type testKey struct {
	id      string
	public  ed25519.PublicKey
	private ed25519.PrivateKey
}

func signedPackageGateway(t *testing.T, trustKey bool) (*Service, *httptest.Server, string, testKey) {
	t.Helper()
	return signedPackageGatewayConfigured(t, trustKey, false, false)
}

func signedPackageGatewayWithArtifactStore(t *testing.T, trustKey bool, artifacts bool) (*Service, *httptest.Server, string, testKey) {
	t.Helper()
	return signedPackageGatewayConfigured(t, trustKey, artifacts, false)
}

func signedPackageGatewayConfigured(t *testing.T, trustKey bool, artifacts bool, allowUnsigned bool) (*Service, *httptest.Server, string, testKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	key := testKey{id: "0102030405060708", public: pub, private: priv}
	settings := store.NewMemoryPlatformSettings()
	if trustKey {
		trusted, _ := json.Marshal([]pkgmeta.TrustedKey{{KeyID: key.id, PublicKey: minisignTestPublicKey(key.id, key.public)}})
		_ = settings.PutPlatformSetting(t.Context(), store.PlatformSetting{Key: componentTrustedKeysKey, Value: string(trusted)})
	}
	svc := newGateway(t, "")
	svc.PlatformSettings = settings
	svc.AllowExtUnsigned = allowUnsigned
	svc.SecretBox = testSecretBox(t)
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", string(rbac.RoleSuperAdmin), nil }
	if artifacts {
		blobs, err := blobstore.NewDiskStore(t.TempDir())
		if err != nil {
			t.Fatalf("disk store: %v", err)
		}
		rt := &realtimehttp.Service{Blobs: blobs}
		rtSrv := httptest.NewServer(svcauth.Middleware(svcauth.KeyFromEnv(), rt.Handler()))
		t.Cleanup(rtSrv.Close)
		svc.Backends.Realtime = rtSrv.URL
	}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	token, _ := login(t, srv, "good")
	return svc, srv, token, key
}

func uploadComponentArtifact(t *testing.T, baseURL, token string, payload []byte) pkgmeta.Artifact {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/platform/artifacts", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload artifact: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload artifact status=%d body=%s, want 200", resp.StatusCode, raw)
	}
	var out struct {
		Key    string `json:"key"`
		SHA256 string `json:"sha256"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode artifact upload: %v", err)
	}
	return pkgmeta.Artifact{URL: baseURL + "/v1/artifacts/" + out.Key, SHA256: out.SHA256}
}
