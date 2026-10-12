package gateway

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

func postComponent(t *testing.T, baseURL, token string, body []byte, signature string) ([]byte, int) {
	t.Helper()
	return postComponentWithRollout(t, baseURL, token, body, signature, "")
}

func postComponentWithRollout(t *testing.T, baseURL, token string, body []byte, signature string, rollout string) ([]byte, int) {
	t.Helper()
	request := componentInstallRequest{Package: string(body), Signature: signature, Rollout: rollout}
	requestBody, _ := json.Marshal(request)
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/platform/components", bytes.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post component: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode
}

func listComponentsForAdmin(t *testing.T, baseURL, token string) []installedComponent {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/platform/components", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list components: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Components []installedComponent `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode components: %v", err)
	}
	return out.Components
}

func componentEndpointStatus(t *testing.T, baseURL, token, method string) int {
	t.Helper()
	path := "/v1/platform/components"
	if method == http.MethodDelete {
		path += "/acme-agents"
	}
	req, _ := http.NewRequest(method, baseURL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s components: %v", method, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func endpointPackageBody(t *testing.T, baseURL, minVersion, maxVersion string) []byte {
	t.Helper()
	body := map[string]any{
		"kind":         pkgmeta.KindCorePackageV1,
		"name":         "acme-agents",
		"version":      "1.4.2",
		"protocol":     protocol.ProtocolRange{Min: minVersion, Max: maxVersion},
		"runtime":      "endpoint",
		"endpoint":     map[string]string{"baseUrl": baseURL},
		"capabilities": []string{"interrupt"},
		"permissions":  map[string]any{"network": []string{"127.0.0.1"}},
		"publisher":    "Acme Corp",
		"description":  "test core",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal package: %v", err)
	}
	return raw
}

func conformingCoreServer(t *testing.T, pass bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == protocol.PathDiscover:
			_ = json.NewEncoder(w).Encode(protocol.Discovery{Name: "acme-agents", Version: "1.4.2", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"}})
		case !pass && r.Method == http.MethodPost && r.URL.Path == protocol.PathWorkspaces:
			_ = json.NewEncoder(w).Encode(protocol.CreateWorkspaceResponse{})
		case r.Method == http.MethodPost && r.URL.Path == protocol.PathWorkspaces:
			_ = json.NewEncoder(w).Encode(protocol.CreateWorkspaceResponse{WorkspaceID: "ws-1"})
		case r.Method == http.MethodGet && r.URL.Path == protocol.PathTargets:
			_ = json.NewEncoder(w).Encode(protocol.ListTargetsResponse{Targets: []protocol.Target{{ID: "local:codex", Provider: "codex"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/core/v1/workspaces/ws-1/sessions":
			_ = json.NewEncoder(w).Encode(protocol.CreateSessionResponse{SessionID: "s-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/core/v1/workspaces/ws-1/sessions/s-1/input":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/core/v1/workspaces/ws-1/sessions/s-1/messages":
			_ = json.NewEncoder(w).Encode(protocol.MessagesResponse{LatestVersion: 1})
		case r.Method == http.MethodGet && r.URL.Path == "/core/v1/workspaces/ws-1/sessions/s-1/turn":
			_ = json.NewEncoder(w).Encode(protocol.PollTurnResponse{Idle: true})
		case r.Method == http.MethodGet && r.URL.Path == "/core/v1/workspaces/__missing_workspace__/sessions/__missing_session__/turn":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(protocol.ErrorEnvelope{Error: protocol.ProtocolError{Code: protocol.ErrorSessionNotFound, Message: "missing session"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func signPackage(priv ed25519.PrivateKey, body []byte) string {
	return signPackageWithKey("0102030405060708", priv, body)
}

func signPackageWithKey(keyID string, priv ed25519.PrivateKey, body []byte) string {
	return "untrusted comment: signature\n" + base64.StdEncoding.EncodeToString(append(minisignTestPrefix(keyID), ed25519.Sign(priv, body)...)) + "\n"
}

func minisignTestPublicKey(keyID string, pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(append(minisignTestPrefix(keyID), pub...))
}

func minisignTestPrefix(keyID string) []byte {
	decoded, err := hex.DecodeString(keyID)
	if err != nil {
		panic(err)
	}
	return append([]byte{'E', 'd'}, decoded...)
}
