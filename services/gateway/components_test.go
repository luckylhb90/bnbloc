package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// The rollout lifecycle: an installed core distributes per its rollout policy —
// auto (everyone, the default), optin (members choose via /v1/components), off
// (hidden everywhere a member looks). The gate must cover all three member
// surfaces: /v1/cores, /v1/components, and the artifact endpoint the desktop
// downloads through.

// subjectVerifier maps the idp token to the subject directly, so one test
// gateway can host distinct users (admin / member) unlike fakeVerifier which
// collapses every login onto one subject.
type subjectVerifier struct{}

func (subjectVerifier) Verify(token string) (auth.Identity, error) {
	if strings.TrimSpace(token) == "" {
		return auth.Identity{}, auth.ErrInvalidToken
	}
	return auth.Identity{Subject: token}, nil
}

func rolloutTestGateway(t *testing.T) (*Service, *httptest.Server, string, string) {
	t.Helper()
	svc, srv, adminToken, _ := signedPackageGatewayConfigured(t, true, true, true)
	svc.Verifier = subjectVerifier{}
	svc.ComponentInstalls = store.NewMemoryComponentInstall()
	svc.TenantResolver = func(id auth.Identity, _ string) (string, string) {
		if id.Subject == "admin" {
			return "home", string(rbac.RoleSuperAdmin)
		}
		return "home", string(rbac.RoleMember)
	}
	svc.MemberCount = func() int { return 3 }
	memberToken, _ := login(t, srv, "member")
	return svc, srv, adminToken, memberToken
}

func installRolloutTestCore(t *testing.T, srv *httptest.Server, adminToken, name string) {
	t.Helper()
	artifact := uploadComponentArtifact(t, srv.URL, adminToken, []byte("rollout sidecar "+name))
	body := sidecarPackageBodyWithArtifact(t, name, artifact)
	raw, status := postComponent(t, srv.URL, adminToken, body, "")
	if status != http.StatusOK {
		t.Fatalf("install status=%d body=%s, want 200", status, raw)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })
}

func setRollout(t *testing.T, srv *httptest.Server, token, name, rollout string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/platform/components/"+name+"/rollout",
		bytes.NewReader([]byte(`{"rollout":"`+rollout+`"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("set rollout: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func listCoreNamesForMember(t *testing.T, srv *httptest.Server, token string) (names []string, byName map[string]coreJSON) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list cores: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list cores status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Cores []coreJSON `json:"cores"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode cores: %v", err)
	}
	byName = map[string]coreJSON{}
	for _, c := range out.Cores {
		names = append(names, c.Name)
		byName[c.Name] = c
	}
	return names, byName
}

type coreJSON struct {
	Name    string `json:"name"`
	Rollout string `json:"rollout"`
}

type componentJSON struct {
	Name             string `json:"name"`
	Rollout          string `json:"rollout"`
	Kind             string `json:"kind"`
	Installed        bool   `json:"installed"`
	Managed          bool   `json:"managed"`
	MembersInstalled int    `json:"membersInstalled"`
}

func listComponentsForMember(t *testing.T, srv *httptest.Server, token string) map[string]componentJSON {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/components", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list components: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list components status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Components []componentJSON `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode components: %v", err)
	}
	byName := map[string]componentJSON{}
	for _, c := range out.Components {
		byName[c.Name] = c
	}
	return byName
}

func memberInstallStatus(t *testing.T, srv *httptest.Server, token, name string, method string) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+"/v1/components/"+name+"/install", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s component install: %v", method, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func artifactStatus(t *testing.T, srv *httptest.Server, token, name string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores/"+name+"/artifact?platform=darwin-arm64", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("artifact: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestComponents_rolloutLifecycle(t *testing.T) {
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installRolloutTestCore(t, srv, adminToken, "rollout-core")

	// Default (no rollout stored): auto — listed, installed, managed.
	if _, ok := listComponentsForMember(t, srv, memberToken)["rollout-core"]; !ok {
		t.Fatal("auto core missing from member components")
	}
	_, byName := listCoreNamesForMember(t, srv, memberToken)
	if byName["rollout-core"].Rollout != "auto" {
		t.Fatalf("cores rollout = %q, want auto", byName["rollout-core"].Rollout)
	}
	if status, body := artifactStatus(t, srv, memberToken, "rollout-core"); status != http.StatusOK {
		t.Fatalf("artifact status=%d body=%s, want 200 for auto", status, body)
	}

	// optin: hidden from the member until they install it.
	if status := setRollout(t, srv, adminToken, "rollout-core", "optin"); status != http.StatusNoContent {
		t.Fatalf("set rollout optin status=%d, want 204", status)
	}
	names, _ := listCoreNamesForMember(t, srv, memberToken)
	if contains(names, "rollout-core") {
		t.Fatal("optin core visible in /v1/cores before member install")
	}
	comps := listComponentsForMember(t, srv, memberToken)
	if c := comps["rollout-core"]; !c.Present() || c.Installed || c.Managed {
		t.Fatalf("components = %+v, want optin core present-but-not-installed", c)
	}
	if status, body := artifactStatus(t, srv, memberToken, "rollout-core"); status != http.StatusNotFound || !strings.Contains(body, "not available") {
		t.Fatalf("artifact status=%d body=%s, want rollout 404", status, body)
	}

	// Member install flips every surface on.
	if status := memberInstallStatus(t, srv, memberToken, "rollout-core", http.MethodPost); status != http.StatusNoContent {
		t.Fatalf("member install status=%d, want 204", status)
	}
	names, _ = listCoreNamesForMember(t, srv, memberToken)
	if !contains(names, "rollout-core") {
		t.Fatal("installed optin core missing from /v1/cores")
	}
	if c := listComponentsForMember(t, srv, memberToken)["rollout-core"]; !c.Installed || c.Managed || c.MembersInstalled != 1 {
		t.Fatalf("components = %+v, want installed, unmanaged, 1 member install", c)
	}
	if status, _ := artifactStatus(t, srv, memberToken, "rollout-core"); status != http.StatusOK {
		t.Fatalf("artifact status=%d, want 200 after member install", status)
	}

	// Member uninstall flips them back off.
	if status := memberInstallStatus(t, srv, memberToken, "rollout-core", http.MethodDelete); status != http.StatusNoContent {
		t.Fatalf("member uninstall status=%d, want 204", status)
	}
	names, _ = listCoreNamesForMember(t, srv, memberToken)
	if contains(names, "rollout-core") {
		t.Fatal("uninstalled optin core still visible in /v1/cores")
	}

	// off: gone from every member surface.
	if status := setRollout(t, srv, adminToken, "rollout-core", "off"); status != http.StatusNoContent {
		t.Fatalf("set rollout off status=%d, want 204", status)
	}
	if _, ok := listComponentsForMember(t, srv, memberToken)["rollout-core"]; ok {
		t.Fatal("off core visible in /v1/components")
	}
	names, _ = listCoreNamesForMember(t, srv, memberToken)
	if contains(names, "rollout-core") {
		t.Fatal("off core visible in /v1/cores")
	}
	if status, _ := artifactStatus(t, srv, memberToken, "rollout-core"); status != http.StatusNotFound {
		t.Fatalf("artifact status=%d, want 404 for off", status)
	}
}

func (c componentJSON) Present() bool { return c.Name != "" }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestComponents_installRequiresOptIn(t *testing.T) {
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installRolloutTestCore(t, srv, adminToken, "auto-core")

	// A member cannot "install" an auto core (it is already theirs) — and the
	// refusal keeps the rollout policy the only distribution authority.
	if status := memberInstallStatus(t, srv, memberToken, "auto-core", http.MethodPost); status != http.StatusBadRequest {
		t.Fatalf("auto install status=%d, want 400", status)
	}
	// Unknown components 404 rather than creating phantom installs.
	if status := memberInstallStatus(t, srv, memberToken, "ghost", http.MethodPost); status != http.StatusNotFound {
		t.Fatalf("ghost install status=%d, want 404", status)
	}
}
