package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func TestComponentKeys_requireSuperAdmin(t *testing.T) {
	svc := newGateway(t, "")
	svc.PlatformSettings = store.NewMemoryPlatformSettings()
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", "org_admin", nil }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/platform/component-keys"},
		{http.MethodPost, "/v1/platform/component-keys"},
		{http.MethodDelete, "/v1/platform/component-keys/abc"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestComponentKeys_unauthenticated(t *testing.T) {
	svc := newGateway(t, "")
	svc.PlatformSettings = store.NewMemoryPlatformSettings()
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/platform/component-keys")
	if err != nil {
		t.Fatalf("get keys: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestComponentKeys_returnNotFoundForLegacyCorePackageKeyRoutes(t *testing.T) {
	// Given
	svc := newGateway(t, "")
	svc.PlatformSettings = store.NewMemoryPlatformSettings()
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", "super_admin", nil }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/v1/platform/core-package-keys"},
		{method: http.MethodPost, path: "/v1/platform/core-package-keys"},
		{method: http.MethodDelete, path: "/v1/platform/core-package-keys/abc"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			// When
			req, _ := http.NewRequest(tt.method, srv.URL+tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("legacy key route: %v", err)
			}
			defer resp.Body.Close()

			// Then
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status=%d, want 404", resp.StatusCode)
			}
		})
	}
}

func TestComponentKeys_addRejectsUnparseableKey(t *testing.T) {
	svc := newGateway(t, "")
	svc.PlatformSettings = store.NewMemoryPlatformSettings()
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", "super_admin", nil }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/platform/component-keys",
		bytes.NewReader([]byte(`{"publicKey":"not-a-real-minisign-key","label":"bad"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("add key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestComponentKeys_addThenListThenDelete(t *testing.T) {
	svc := newGateway(t, "")
	svc.PlatformSettings = store.NewMemoryPlatformSettings()
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", "super_admin", nil }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	// A valid minisign public key (the updater's own public key: algorithm Ed).
	pub := `{"publicKey":"untrusted comment: minisign public key: 1EB0D2DD9D61EB22\nRWQi62Gd3dKwHuZheHY4MOD3ng/Dyi8xlKnRPp4eagb0Po236QlHXOUj\n","label":"updater"}`

	// add
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/platform/component-keys", bytes.NewReader([]byte(pub)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("add key: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add key status = %d, want 200", resp.StatusCode)
	}

	// duplicate add is a conflict
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/platform/component-keys", bytes.NewReader([]byte(pub)))
	req2.Header.Set("Authorization", "Bearer "+token)
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate add status = %d, want 409", resp2.StatusCode)
	}

	// delete
	req3, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/platform/component-keys/22eb619dddd2b01e", nil)
	req3.Header.Set("Authorization", "Bearer "+token)
	resp3, _ := http.DefaultClient.Do(req3)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp3.StatusCode)
	}
}
