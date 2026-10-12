package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/svcauth"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

func getArtifactStatus(t *testing.T, srv *httptest.Server, token string, art pkgmeta.Artifact) int {
	t.Helper()
	key := artifactURLKey(art.URL)
	if key == "" {
		t.Fatalf("artifact URL %q has no gateway key", art.URL)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/artifacts/"+key, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func doJSON(t *testing.T, srv *httptest.Server, method, path, token, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// BR-19: the blob store behind /v1/artifacts is shared, so a member may only
// download keys referenced by a component published to them; any other staged
// key is a 404. Super-admins may fetch any staged key.
func TestArtifactDownload_servesOnlyEntitledKeys(t *testing.T) {
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	const name = "entitled-core"
	published := uploadComponentArtifact(t, srv.URL, adminToken, []byte("published sidecar bytes"))
	raw, status := postComponent(t, srv.URL, adminToken, sidecarPackageBodyWithArtifact(t, name, published), "")
	if status != http.StatusOK {
		t.Fatalf("install status=%d body=%s", status, raw)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })
	stray := uploadComponentArtifact(t, srv.URL, adminToken, []byte("staged but never published"))

	if got := getArtifactStatus(t, srv, memberToken, published); got != http.StatusOK {
		t.Fatalf("member download of a published artifact = %d, want 200", got)
	}
	if got := getArtifactStatus(t, srv, memberToken, stray); got != http.StatusNotFound {
		t.Fatalf("member download of an unreferenced key = %d, want 404", got)
	}
	if got := getArtifactStatus(t, srv, adminToken, stray); got != http.StatusOK {
		t.Fatalf("super-admin download of a staged key = %d, want 200", got)
	}

	// Opt-in: hidden until the member installs it.
	if status := setRollout(t, srv, adminToken, name, "optin"); status != http.StatusNoContent {
		t.Fatalf("set optin = %d", status)
	}
	if got := getArtifactStatus(t, srv, memberToken, published); got != http.StatusNotFound {
		t.Fatalf("member download of an un-installed opt-in artifact = %d, want 404", got)
	}
	if status := doJSON(t, srv, http.MethodPost, "/v1/components/"+name+"/install", memberToken, ""); status != http.StatusNoContent {
		t.Fatalf("member opt-in install = %d", status)
	}
	if got := getArtifactStatus(t, srv, memberToken, published); got != http.StatusOK {
		t.Fatalf("member download after opt-in = %d, want 200", got)
	}

	// Off: nobody but the super-admin.
	if status := setRollout(t, srv, adminToken, name, "off"); status != http.StatusNoContent {
		t.Fatalf("set off = %d", status)
	}
	if got := getArtifactStatus(t, srv, memberToken, published); got != http.StatusNotFound {
		t.Fatalf("member download of an off artifact = %d, want 404", got)
	}
}

// BR-24: a core and a plugin may share a name; a name-only lookup must refuse
// to guess, and ?kind= picks exactly one.
func TestComponentLookup_disambiguatesByKind(t *testing.T) {
	svc, srv, adminToken, _ := rolloutTestGateway(t)
	const name = "twin"
	coreArt := uploadComponentArtifact(t, srv.URL, adminToken, []byte("twin core"))
	if raw, status := postComponent(t, srv.URL, adminToken, sidecarPackageBodyWithArtifact(t, name, coreArt), ""); status != http.StatusOK {
		t.Fatalf("core install status=%d body=%s", status, raw)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })
	pluginArt := uploadComponentArtifact(t, srv.URL, adminToken, []byte("twin plugin"))
	if raw, status := postComponent(t, srv.URL, adminToken, pluginPackageBodyWithArtifact(t, name, "http://127.0.0.1:7191", []string{"chat_message.v1"}, pluginArt), ""); status != http.StatusOK {
		t.Fatalf("plugin install status=%d body=%s", status, raw)
	}
	t.Cleanup(func() { _ = extreg.Unregister(pkgmeta.KindPlugin, name) })

	if status := doJSON(t, srv, http.MethodDelete, "/v1/platform/components/"+name, adminToken, ""); status != http.StatusConflict {
		t.Fatalf("ambiguous uninstall = %d, want 409", status)
	}
	if status := doJSON(t, srv, http.MethodDelete, "/v1/platform/components/"+name+"?kind=plugin", adminToken, ""); status != http.StatusNoContent {
		t.Fatalf("kind-qualified uninstall = %d, want 204", status)
	}
	if _, ok, _ := svc.findComponentRecord(t.Context(), core.KindCore, name); !ok {
		t.Fatal("uninstalling the plugin removed the same-named core")
	}
	if _, ok, _ := svc.findComponentRecord(t.Context(), pkgmeta.KindPlugin, name); ok {
		t.Fatal("plugin record still present after kind-qualified uninstall")
	}
	// Now unambiguous again: the bare name resolves to the core.
	if status := setRollout(t, srv, adminToken, name, "optin"); status != http.StatusNoContent {
		t.Fatalf("bare-name rollout after disambiguation = %d, want 204", status)
	}
}

// BR-6: the tenant overlay has explicit, fail-closed semantics at the edge.
func TestSetComponentTenants_rejectsAmbiguousScopes(t *testing.T) {
	_, srv, adminToken, _ := rolloutTestGateway(t)
	const name = "scoped-core"
	installRolloutTestCore(t, srv, adminToken, name)
	path := "/v1/platform/components/" + name + "/tenants"
	for _, body := range []string{`{"enabledTenants":[]}`, `{}`, `{"enabledTenants":[" "]}`, `{"allTenants":true,"enabledTenants":["home"]}`} {
		if status := doJSON(t, srv, http.MethodPut, path, adminToken, body); status != http.StatusBadRequest {
			t.Fatalf("PUT tenants %s = %d, want 400", body, status)
		}
	}
	if status := doJSON(t, srv, http.MethodPut, path, adminToken, `{"enabledTenants":["other"]}`); status != http.StatusNoContent {
		t.Fatalf("restrict = %d, want 204", status)
	}
	if status := doJSON(t, srv, http.MethodPut, path, adminToken, `{"allTenants":true}`); status != http.StatusNoContent {
		t.Fatalf("allTenants = %d, want 204", status)
	}
}

// BR-9: /v1/local-agent/run dials only trusted core endpoints; a caller
// address override is a single-host dev feature, and no target is guessed.
func TestLocalAgentRun_refusesOverrideAndRequiresTarget(t *testing.T) {
	t.Setenv("BNBLOC_DEV_DISCOVERY", "")
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installConfiguredEndpointCore(t, srv, adminToken)

	body := `{"core":"acme-agents","agentTargetId":"local:codex","localAgentUrl":"http://127.0.0.1:1","prompt":"hi"}`
	if status := doJSON(t, srv, http.MethodPost, "/v1/local-agent/run", memberToken, body); status != http.StatusForbidden {
		t.Fatalf("override on a non-dev gateway = %d, want 403", status)
	}
	if status := doJSON(t, srv, http.MethodGet, "/v1/local-agent/targets?localAgentUrl=http://127.0.0.1:1", memberToken, ""); status != http.StatusForbidden {
		t.Fatalf("targets override on a non-dev gateway = %d, want 403", status)
	}
	if status := doJSON(t, srv, http.MethodPost, "/v1/local-agent/run", memberToken, `{"core":"acme-agents","prompt":"hi"}`); status != http.StatusBadRequest {
		t.Fatalf("run without agentTargetId = %d, want 400", status)
	}
}

func TestLocalAgentOverride_devAllowsOnlyLoopbackIPs(t *testing.T) {
	t.Setenv("BNBLOC_DEV_DISCOVERY", "1")
	for _, ok := range []string{"http://127.0.0.1:7777", "http://[::1]:7777", "https://127.0.0.2"} {
		if _, err := localAgentOverride(ok, "tok"); err != nil {
			t.Fatalf("override %q refused in dev: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://localhost:7777", "http://host.docker.internal:7777", "http://mysql:3306", "http://10.0.0.5", "http://169.254.169.254", "file:///etc/passwd"} {
		if _, err := localAgentOverride(bad, ""); err == nil {
			t.Fatalf("override %q accepted; want refused", bad)
		}
	}
	if ep, err := localAgentOverride("  ", ""); err != nil || ep.OK() {
		t.Fatalf("empty override = %+v, %v; want no override", ep, err)
	}
	t.Setenv("BNBLOC_DEV_DISCOVERY", "")
	if _, err := localAgentOverride("http://127.0.0.1:7777", ""); err == nil || !strings.Contains(err.Error(), "single-host dev") {
		t.Fatalf("override outside dev = %v; want refused", err)
	}
}

// lockProbeProvider's discovery checks whether coresMu is free while it runs:
// /v1/cores must not hold the cores lock across network discovery (BR-18).
type lockProbeProvider struct {
	fakeCoreProvider
	svc      *Service
	lockFree *bool
}

func (p lockProbeProvider) Discover(context.Context) (core.Endpoint, bool) {
	if p.svc.coresMu.TryLock() {
		*p.lockFree = true
		p.svc.coresMu.Unlock()
	}
	return core.Endpoint{BaseURL: "http://127.0.0.1:9"}, true
}

func TestHandleCores_discoversOutsideCoresLock(t *testing.T) {
	svc, srv, _, memberToken := rolloutTestGateway(t)
	lockFree := false
	svc.coresMu.Lock()
	svc.Cores = append(svc.Cores, coreset.Entry{Name: "probe-core", Provider: lockProbeProvider{fakeCoreProvider: fakeCoreProvider{name: "probe-core"}, svc: svc, lockFree: &lockFree}})
	svc.coresMu.Unlock()

	if status := doJSON(t, srv, http.MethodGet, "/v1/cores", memberToken, ""); status != http.StatusOK {
		t.Fatalf("/v1/cores = %d", status)
	}
	if !lockFree {
		t.Fatal("core discovery ran while coresMu was held")
	}
	if entry := svc.coreByName("probe-core"); entry == nil || entry.Endpoint.BaseURL != "http://127.0.0.1:9" {
		t.Fatalf("discovered endpoint not written back: %+v", entry)
	}
}

// The gateway's own blob reads assert the svcauth service-subject convention,
// not the local daemon's identity.
func TestGatewayBlobReadAssertsServiceSubject(t *testing.T) {
	var got string
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Bnbloc-User")
		_, _ = w.Write([]byte("bytes"))
	}))
	defer rt.Close()
	svc := &Service{Backends: Backends{Realtime: rt.URL}}
	if _, err := svc.readArtifactBlob(context.Background(), "sha256/k", 64); err != nil {
		t.Fatal(err)
	}
	if got != svcauth.ServiceSubject("gateway") {
		t.Fatalf("X-Bnbloc-User = %q, want %q", got, svcauth.ServiceSubject("gateway"))
	}
}

// BR-24 (member routes): POST/DELETE /v1/components/{name}/install refuse an
// ambiguous name with 409 and honor ?kind=.
func TestMemberComponentInstall_disambiguatesByKind(t *testing.T) {
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	const name = "twin-member"
	coreArt := uploadComponentArtifact(t, srv.URL, adminToken, []byte("twin member core"))
	if raw, status := postComponent(t, srv.URL, adminToken, sidecarPackageBodyWithArtifact(t, name, coreArt), ""); status != http.StatusOK {
		t.Fatalf("core install status=%d body=%s", status, raw)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })
	pluginArt := uploadComponentArtifact(t, srv.URL, adminToken, []byte("twin member plugin"))
	if raw, status := postComponent(t, srv.URL, adminToken, pluginPackageBodyWithArtifact(t, name, "http://127.0.0.1:7191", []string{"chat_message.v1"}, pluginArt), ""); status != http.StatusOK {
		t.Fatalf("plugin install status=%d body=%s", status, raw)
	}
	t.Cleanup(func() { _ = extreg.Unregister(pkgmeta.KindPlugin, name) })
	for _, kind := range []string{"core", "plugin"} {
		if status := doJSON(t, srv, http.MethodPut, "/v1/platform/components/"+name+"/rollout?kind="+kind, adminToken, `{"rollout":"optin"}`); status != http.StatusNoContent {
			t.Fatalf("set %s optin = %d", kind, status)
		}
	}
	install := "/v1/components/" + name + "/install"
	if status := doJSON(t, srv, http.MethodPost, install, memberToken, ""); status != http.StatusConflict {
		t.Fatalf("ambiguous member install = %d, want 409", status)
	}
	if status := doJSON(t, srv, http.MethodPost, install+"?kind=core", memberToken, ""); status != http.StatusNoContent {
		t.Fatalf("kind-qualified member install = %d, want 204", status)
	}
	if status := doJSON(t, srv, http.MethodDelete, install, memberToken, ""); status != http.StatusConflict {
		t.Fatalf("ambiguous member uninstall = %d, want 409", status)
	}
	if status := doJSON(t, srv, http.MethodDelete, install+"?kind=core", memberToken, ""); status != http.StatusNoContent {
		t.Fatalf("kind-qualified member uninstall = %d, want 204", status)
	}
}

// The trusted-keys listing tells the publishing UI whether signatures are
// required, mirroring the gateway's own install policy.
func TestComponentKeys_reportSignatureRequirement(t *testing.T) {
	for _, allowUnsigned := range []bool{false, true} {
		svc, srv, adminToken, _ := rolloutTestGateway(t)
		svc.AllowExtUnsigned = allowUnsigned
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/platform/component-keys", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			SignatureRequired *bool `json:"signatureRequired"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if body.SignatureRequired == nil || *body.SignatureRequired != !allowUnsigned {
			t.Fatalf("AllowExtUnsigned=%v: signatureRequired = %v, want %v", allowUnsigned, body.SignatureRequired, !allowUnsigned)
		}
	}
}
