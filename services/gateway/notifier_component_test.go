package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func notifierPackageBody(t *testing.T, name, baseURL string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"kind": pkgmeta.KindNotifierPackageV1, "name": name, "version": "0.1.0", "publisher": "Acme Corp",
		"runtime": pkgmeta.RuntimeEndpoint, "contract": pkgmeta.NotifierContractV1,
		"endpoint":    map[string]any{"baseUrl": baseURL},
		"permissions": map[string]any{"network": []string{"127.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func platformPost(t *testing.T, url, token string) ([]byte, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode
}

// A notifier installs through the shared component path (probed, never
// activated by the gateway), its signing secret is shown once, rotatable,
// usable for a super-admin test send, and removed with the component.
func TestNotifierComponentLifecycle(t *testing.T) {
	var mu sync.Mutex
	var sigs []string
	var bodies [][]byte
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifier/discover":
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "corp", "contract": pkgmeta.NotifierContractV1})
		case "/notifier/v1/notifications":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			sigs = append(sigs, r.Header.Get(wire.NotificationSignatureHeader))
			bodies = append(bodies, b)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(wire.NotificationDeliveryResponse{Results: []wire.NotificationRecipientResult{{UserID: "x", OK: true}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer receiver.Close()
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	body := notifierPackageBody(t, "corp", receiver.URL+"/notifier")

	raw, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))
	var installed wire.PublishedComponent
	_ = json.Unmarshal(raw, &installed)
	if status != http.StatusOK || installed.Kind != "notifier" || !strings.HasPrefix(installed.Secret, "whsec_") {
		t.Fatalf("install = %d %s", status, raw)
	}
	// The list never carries the secret.
	listRaw, _ := json.Marshal(listComponentsForAdmin(t, srv.URL, token))
	if strings.Contains(string(listRaw), installed.Secret) {
		t.Fatal("component list leaked the signing secret")
	}
	// Reinstalling (upgrade) keeps the secret and does not show it again.
	raw, status = postComponent(t, srv.URL, token, body, signPackage(key.private, body))
	var again wire.PublishedComponent
	_ = json.Unmarshal(raw, &again)
	if status != http.StatusOK || again.Secret != "" {
		t.Fatalf("reinstall = %d %s", status, raw)
	}

	// Test send: signed with the component secret, to the calling admin.
	raw, status = platformPost(t, srv.URL+"/v1/platform/components/corp/test?kind=notifier", token)
	var res wire.NotificationTestResult
	_ = json.Unmarshal(raw, &res)
	if status != http.StatusOK || !res.Delivered {
		t.Fatalf("test = %d %s", status, raw)
	}
	mu.Lock()
	if err := notify.Verify(sigs[0], installed.Secret, bodies[0], time.Now(), notify.SignatureTolerance); err != nil {
		t.Fatalf("test signature: %v", err)
	}
	mu.Unlock()

	// Rotation returns a new secret once; the next test is signed with it.
	raw, status = platformPost(t, srv.URL+"/v1/platform/components/corp/secret/rotate?kind=notifier", token)
	var rotated struct{ Secret string }
	_ = json.Unmarshal(raw, &rotated)
	if status != http.StatusOK || rotated.Secret == "" || rotated.Secret == installed.Secret {
		t.Fatalf("rotate = %d %s", status, raw)
	}
	_, _ = platformPost(t, srv.URL+"/v1/platform/components/corp/test", token)
	mu.Lock()
	if err := notify.Verify(sigs[1], rotated.Secret, bodies[1], time.Now(), notify.SignatureTolerance); err != nil {
		t.Fatalf("rotated signature: %v", err)
	}
	mu.Unlock()

	// Uninstall deletes the secret.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/platform/components/corp?kind=notifier", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("uninstall = %v %v", resp, err)
	}
	resp.Body.Close()
	if _, ok, _ := (&notify.SecretStore{Settings: svc.PlatformSettings}).Get(t.Context(), "corp"); ok {
		t.Fatal("uninstall kept the signing secret")
	}
}

func TestNotifierInstallRefusesEndpointWithoutDiscover(t *testing.T) {
	receiver := httptest.NewServer(http.NotFoundHandler())
	defer receiver.Close()
	_, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	body := notifierPackageBody(t, "corp-off", receiver.URL+"/notifier")
	raw, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "notifier discover probe status 404") {
		t.Fatalf("install = %d %s; want discover probe failure", status, raw)
	}
}

func TestNotifierRoutesRejectOtherKinds(t *testing.T) {
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	authSrv := healthyAuthProviderServer(t)
	body := authProviderPackageBody(t, "sso-x", authSrv.URL)
	t.Cleanup(func() { _ = svc.corePipeline().Uninstall(t.Context(), "auth-provider", "sso-x", "test") })
	if raw, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body)); status != http.StatusOK {
		t.Fatalf("install auth-provider = %d %s", status, raw)
	}
	for _, path := range []string{"/secret/rotate", "/test"} {
		if raw, status := platformPost(t, srv.URL+"/v1/platform/components/sso-x"+path, token); status != http.StatusBadRequest {
			t.Fatalf("%s on an auth-provider = %d %s, want 400", path, status, raw)
		}
	}
}
