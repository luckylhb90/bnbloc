package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/store"
)

type explicitInstalledProvider struct{ fakeCoreProvider }

func (p explicitInstalledProvider) CreateWorkspace(context.Context, core.Endpoint) (string, error) {
	return "ws-explicit", nil
}

func (p explicitInstalledProvider) CreateSession(context.Context, core.Endpoint, core.SessionRequest) (core.SessionResult, error) {
	return core.SessionResult{SessionID: "s-explicit", TurnID: "t-explicit", Phase: "running"}, nil
}

func (p explicitInstalledProvider) ListTargets(context.Context, core.Endpoint) ([]core.Target, error) {
	return []core.Target{{ID: "local:codex", Provider: "codex"}}, nil
}

func TestLocalAgentTargets_hidesOffComponentCore(t *testing.T) {
	// Given
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installRolloutTestCore(t, srv, adminToken, "hidden-target-core")
	if status := setRollout(t, srv, adminToken, "hidden-target-core", "off"); status != http.StatusNoContent {
		t.Fatalf("set rollout status=%d, want 204", status)
	}

	// When
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/local-agent/targets", nil)
	req.Header.Set("Authorization", "Bearer "+memberToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	defer resp.Body.Close()

	// Then
	raw := readBodyString(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "hidden-target-core") {
		t.Fatalf("off component core leaked from targets: %s", raw)
	}
}

func TestLocalAgentRun_rejectsUninstalledOptInCore(t *testing.T) {
	// Given
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installRolloutTestCore(t, srv, adminToken, "optin-run-core")
	if status := setRollout(t, srv, adminToken, "optin-run-core", "optin"); status != http.StatusNoContent {
		t.Fatalf("set rollout status=%d, want 204", status)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/local-agent/run", strings.NewReader(`{"core":"optin-run-core","prompt":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+memberToken)

	// When
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer resp.Body.Close()

	// Then
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", resp.StatusCode, readBodyString(t, resp))
	}
}

func TestLocalAgentTargets_hidesConfiguredPersistedOptInCore_whenNotInstalled(t *testing.T) {
	// Given
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installConfiguredEndpointCore(t, srv, adminToken)
	if status := setRollout(t, srv, adminToken, "acme-agents", "optin"); status != http.StatusNoContent {
		t.Fatalf("set rollout status=%d, want 204", status)
	}

	// When
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/local-agent/targets", nil)
	req.Header.Set("Authorization", "Bearer "+memberToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	defer resp.Body.Close()

	// Then
	raw := readBodyString(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "acme-agents") {
		t.Fatalf("uninstalled opt-in configured core leaked from targets: %s", raw)
	}
}

func TestLocalAgentRun_rejectsConfiguredPersistedOffCore(t *testing.T) {
	// Given
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installConfiguredEndpointCore(t, srv, adminToken)
	if status := setRollout(t, srv, adminToken, "acme-agents", "off"); status != http.StatusNoContent {
		t.Fatalf("set rollout status=%d, want 204", status)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/local-agent/run", strings.NewReader(`{"core":"acme-agents","prompt":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+memberToken)

	// When
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer resp.Body.Close()

	// Then
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", resp.StatusCode, readBodyString(t, resp))
	}
}

func installConfiguredEndpointCore(t *testing.T, srv *httptest.Server, adminToken string) {
	t.Helper()
	coreSrv := conformingCoreServer(t, true)
	body := endpointPackageBody(t, coreSrv.URL, "1.0", "1.0")
	raw, status := postComponent(t, srv.URL, adminToken, body, "")
	if status != http.StatusOK {
		t.Fatalf("install endpoint status=%d body=%s, want 200", status, raw)
	}
	t.Cleanup(func() { _ = core.Unregister("acme-agents") })
}

func TestLocalAgentRun_allowsConfiguredBuiltinWithoutComponentRecord(t *testing.T) {
	// Given
	const name = "configured-builtin-run-core"
	core.Register(name, func() core.Provider { return fakeCoreProvider{name: name} })
	t.Cleanup(func() { _ = core.Unregister(name) })
	gw := newGateway(t, "")
	provider, err := core.New(name)
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	gw.Cores = []coreset.Entry{{Name: name, Provider: provider, Endpoint: core.Endpoint{BaseURL: "http://127.0.0.1:9999"}}}
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	// When
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cores: %v", err)
	}
	defer resp.Body.Close()

	// Then
	raw := readBodyString(t, resp)
	if !strings.Contains(raw, name) {
		t.Fatalf("configured builtin core missing from /v1/cores: %s", raw)
	}
}

func TestExplicitConfiguredInstalledCore_visibleWithoutComponentRecord(t *testing.T) {
	// Given
	const name = "explicit-installed-core"
	provider := explicitInstalledProvider{fakeCoreProvider{name: name}}
	if err := core.Reregister(name, func() core.Provider { return provider }, core.CoreExtension{Backend: core.BackendSidecar, Source: core.SourceInstalled, Removable: true}); err != nil {
		t.Fatalf("reregister: %v", err)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })
	room := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer room.Close()
	gw := newGateway(t, room.URL)
	gw.PlatformSettings = store.NewMemoryPlatformSettings()
	gw.Cores = []coreset.Entry{{Name: name, Provider: provider, Endpoint: core.Endpoint{BaseURL: "http://127.0.0.1:9999"}, Explicit: true}}
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	coresReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores", nil)
	coresReq.Header.Set("Authorization", "Bearer "+token)
	coresResp, err := http.DefaultClient.Do(coresReq)
	if err != nil {
		t.Fatalf("cores: %v", err)
	}
	coresRaw := readBodyString(t, coresResp)
	coresResp.Body.Close()
	if !strings.Contains(coresRaw, name) {
		t.Fatalf("explicit configured core missing from /v1/cores: %s", coresRaw)
	}

	targetsReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/local-agent/targets", nil)
	targetsReq.Header.Set("Authorization", "Bearer "+token)
	targetsResp, err := http.DefaultClient.Do(targetsReq)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	targetsRaw := readBodyString(t, targetsResp)
	targetsResp.Body.Close()
	if !strings.Contains(targetsRaw, name) || !strings.Contains(targetsRaw, name+"/local:codex") {
		t.Fatalf("explicit configured core missing from targets: %s", targetsRaw)
	}

	runReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/local-agent/run", strings.NewReader(`{"core":"`+name+`","agentTargetId":"local:codex","prompt":"hello"}`))
	runReq.Header.Set("Authorization", "Bearer "+token)
	runResp, err := http.DefaultClient.Do(runReq)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer runResp.Body.Close()
	if runResp.StatusCode != http.StatusOK {
		t.Fatalf("run status=%d body=%s, want 200", runResp.StatusCode, readBodyString(t, runResp))
	}
}

func readBodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}
