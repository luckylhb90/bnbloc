package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// testSecretBox is a deployment's secret store key: component settings and
// endpoint components' credentials are sealed with it.
func testSecretBox(t *testing.T) *sealbox.Box {
	t.Helper()
	box, err := sealbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// endpointSettingsDecl is an auth provider's declaration: a required URL,
// a secret, a switch with a default, and an organization setting.
var endpointSettingsDecl = []map[string]any{
	{"key": "metadataUrl", "label": "IdP metadata URL", "type": "url", "scope": "platform", "required": true},
	{"key": "spKey", "label": "SP key", "type": "secret", "scope": "platform"},
	{"key": "skipTls", "label": "Skip TLS verification", "type": "boolean", "scope": "platform", "default": "false"},
	{"key": "groupAttr", "label": "Groups attribute", "type": "text", "scope": "org"},
}

// installAskingAuthProvider installs a signed auth provider declaring
// settings and returns its credential (shown once by the install).
func installAskingAuthProvider(t *testing.T, svc *Service, srvURL, token string, key testKey, name string) string {
	t.Helper()
	authSrv := healthyAuthProviderServer(t)
	var pkg map[string]any
	_ = json.Unmarshal(authProviderPackageBody(t, name, authSrv.URL), &pkg)
	pkg["settings"] = endpointSettingsDecl
	body, _ := json.Marshal(pkg)
	raw, status := postComponent(t, srvURL, token, body, signPackage(key.private, body))
	var installed wire.PublishedComponent
	_ = json.Unmarshal(raw, &installed)
	if status != http.StatusOK || !strings.HasPrefix(installed.Secret, "whsec_") {
		t.Fatalf("install = %d %s", status, raw)
	}
	t.Cleanup(func() { _ = svc.corePipeline().Uninstall(t.Context(), extreg.KindAuthProvider, name, "test") })
	return installed.Secret
}

// askSettings is an endpoint component's ask, signed with secret at at.
func askSettings(t *testing.T, srvURL, component, secret string, at time.Time) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srvURL+wire.ComponentSettingsPath, nil)
	if component != "" {
		req.Header.Set(wire.ComponentHeader, component)
	}
	if secret != "" {
		req.Header.Set(wire.NotificationSignatureHeader, notify.Sign(secret, at, []byte("GET "+wire.ComponentSettingsPath)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// An auth provider gets its credential at install (shown once), asks for
// its settings with it, and is answered its platform values and the
// organization values of the organizations its install serves; a super
// admin rotates it like a notifier's.
func TestEndpointComponentAsksForItsSettings(t *testing.T) {
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	secret := installAskingAuthProvider(t, svc, srv.URL, token, key, "sso-ask")
	stored, _, _ := svc.PlatformSettings.GetPlatformSetting(t.Context(), "component.credential/auth-provider/sso-ask")
	if stored.Value == "" || strings.Contains(stored.Value, secret) {
		t.Fatalf("the credential is not sealed at rest: %q", stored.Value)
	}
	ctx := t.Context()
	st := svc.componentSettings()
	set := func(scope, tenant string, values map[string]string) {
		t.Helper()
		if _, err := st.Update(ctx, scope, tenant, "auth-provider", "sso-ask", "root", func(map[string]string) (map[string]string, error) { return values, nil }); err != nil {
			t.Fatal(err)
		}
	}
	code, raw := askSettings(t, srv.URL, "auth-provider/sso-ask", secret, time.Now())
	var first wire.EndpointComponentSettings
	if code != http.StatusOK || json.Unmarshal(raw, &first) != nil || len(first.Platform) != 0 || !reflect.DeepEqual(first.Missing.Platform, []string{"metadataUrl"}) {
		t.Fatalf("nothing set = %d %s", code, raw)
	}
	set(pkgmeta.SettingScopePlatform, "", map[string]string{"metadataUrl": "https://idp.example.com/meta", "spKey": "-----BEGIN PRIVATE KEY-----"})
	set(pkgmeta.SettingScopeOrg, "acme", map[string]string{"groupAttr": "memberOf"})
	set(pkgmeta.SettingScopeOrg, "nimbus", map[string]string{"groupAttr": "groups"})
	// The organization's settings list carries it (it is in no member's
	// catalog) while the install serves the caller's organization.
	orgList := func() []wire.OrgSettingsComponent {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/tenant/component-settings", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var reply wire.OrgSettingsComponentsReply
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&reply) != nil {
			t.Fatalf("org settings list = %d", resp.StatusCode)
		}
		return reply.Components
	}
	if list := orgList(); len(list) != 1 || list[0].Kind != "auth-provider" || list[0].Name != "sso-ask" || len(list[0].Settings) != 4 || len(list[0].SettingsMissing) != 0 {
		t.Fatalf("org settings list = %+v", list)
	}
	// The install serves acme only: nimbus's values are not its.
	if err := svc.corePipeline().SetTenants(ctx, extreg.KindAuthProvider, "sso-ask", []string{"acme"}, "root"); err != nil {
		t.Fatal(err)
	}
	if list := orgList(); len(list) != 0 {
		t.Fatalf("an organization the install does not serve lists it: %+v", list)
	}
	code, raw = askSettings(t, srv.URL, "auth-provider/sso-ask", secret, time.Now())
	var got wire.EndpointComponentSettings
	if code != http.StatusOK || json.Unmarshal(raw, &got) != nil {
		t.Fatalf("ask = %d %s", code, raw)
	}
	if got.Kind != "auth-provider" || got.Name != "sso-ask" || got.Version == "" || got.Version == first.Version ||
		!reflect.DeepEqual(got.Platform, map[string]string{"metadataUrl": "https://idp.example.com/meta", "spKey": "-----BEGIN PRIVATE KEY-----"}) ||
		!reflect.DeepEqual(got.Orgs, map[string]map[string]string{"acme": {"groupAttr": "memberOf"}}) ||
		len(got.Missing.Platform) != 0 {
		t.Fatalf("answer = %+v", got)
	}

	// A rotation answers a new credential once; the old one stops working.
	rotRaw, status := platformPost(t, srv.URL+"/v1/platform/components/sso-ask/secret/rotate?kind=auth-provider", token)
	var rotated struct{ Secret string }
	_ = json.Unmarshal(rotRaw, &rotated)
	if status != http.StatusOK || !strings.HasPrefix(rotated.Secret, "whsec_") || rotated.Secret == secret {
		t.Fatalf("rotate = %d %s", status, rotRaw)
	}
	if code, _ := askSettings(t, srv.URL, "auth-provider/sso-ask", rotated.Secret, time.Now()); code != http.StatusOK {
		t.Fatalf("ask with the rotated credential = %d", code)
	}
	if code, _ := askSettings(t, srv.URL, "auth-provider/sso-ask", secret, time.Now()); code != http.StatusUnauthorized {
		t.Fatalf("ask with the old credential = %d, want 401", code)
	}

	// The platform's form says the component asks for its values, and
	// shows a secret only as set.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/platform/components/sso-ask/settings?kind=auth-provider", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var view wire.ComponentSettingsView
	if json.NewDecoder(resp.Body).Decode(&view) != nil || !view.Asks || view.ReachesMembers || len(view.Fields) != 3 || view.Fields[1].Value != "" || !view.Fields[1].Set {
		t.Fatalf("view = %d %+v", resp.StatusCode, view)
	}
}

// The ask is refused without the component's own valid, fresh signature.
func TestEndpointSettingsAskRefusals(t *testing.T) {
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	secret := installAskingAuthProvider(t, svc, srv.URL, token, key, "sso-refuse")
	other := installAskingAuthProvider(t, svc, srv.URL, token, key, "sso-other")
	now := time.Now()
	for name, c := range map[string]struct {
		component, secret string
		at                time.Time
		want              int
	}{
		"no component header":       {"", secret, now, http.StatusUnauthorized},
		"a name without a kind":     {"sso-refuse", secret, now, http.StatusUnauthorized},
		"a kind with no credential": {"plugin/sso-refuse", secret, now, http.StatusUnauthorized},
		"no signature":              {"auth-provider/sso-refuse", "", now, http.StatusUnauthorized},
		"a wrong secret":            {"auth-provider/sso-refuse", "whsec_00", now, http.StatusUnauthorized},
		"a stale timestamp":         {"auth-provider/sso-refuse", secret, now.Add(-6 * time.Minute), http.StatusUnauthorized},
		"a future timestamp":        {"auth-provider/sso-refuse", secret, now.Add(6 * time.Minute), http.StatusUnauthorized},
		"not installed":             {"auth-provider/sso-nobody", secret, now, http.StatusForbidden},
		"the name as a notifier":    {"notifier/sso-refuse", secret, now, http.StatusForbidden},
		"another component's name":  {"auth-provider/sso-other", secret, now, http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			if code, raw := askSettings(t, srv.URL, c.component, c.secret, c.at); code != c.want || strings.Contains(string(raw), "metadataUrl") {
				t.Fatalf("ask = %d %s, want %d", code, raw, c.want)
			}
		})
	}
	// A signature over another path does not verify here.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+wire.ComponentSettingsPath, nil)
	req.Header.Set(wire.ComponentHeader, "auth-provider/sso-other")
	req.Header.Set(wire.NotificationSignatureHeader, notify.Sign(other, now, []byte("GET /v1/notifier/replies")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a signature over another path = %d", resp.StatusCode)
	}
	if code, _ := askSettings(t, srv.URL, "auth-provider/sso-other", other, now); code != http.StatusOK {
		t.Fatalf("the other component's own ask = %d", code)
	}
}

// Without the secret store key an endpoint component cannot hold a
// credential: its install is refused before anything is stored.
func TestEndpointInstallNeedsTheSecretStoreKey(t *testing.T) {
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	svc.SecretBox = nil
	body := authProviderPackageBody(t, "sso-keyless", healthyAuthProviderServer(t).URL)
	raw, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))
	if status != http.StatusConflict || !strings.Contains(string(raw), sealbox.KeyEnv) {
		t.Fatalf("keyless install = %d %s", status, raw)
	}
	if _, found, _ := svc.findComponentRecord(t.Context(), extreg.KindAuthProvider, "sso-keyless"); found {
		t.Fatal("the refused install stored a record")
	}
}

// TestComponentCallSingleOwner: a component's signed call to the gateway is
// verified by verifyComponentCall alone (component_credential.go), and a
// component credential is read and written only through
// internal/componentcred: no handler checks a signature or reads the
// stored row itself.
func TestComponentCallSingleOwner(t *testing.T) {
	verify := regexp.MustCompile(`notify\.Verify\(`)
	rows := regexp.MustCompile(`"component\.credential/|"notify\.secret/`)
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "cmd", "internal"}}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "services/gateway/") && rel != "services/gateway/component_credential.go" && verify.Match(src) {
			t.Errorf("%s verifies a component's signature; verifyComponentCall (component_credential.go) is the one owner", rel)
		}
		if rows.Match(src) && !strings.HasPrefix(rel, "internal/componentcred/") {
			t.Errorf("%s names a component credential's row; internal/componentcred is its one owner", rel)
		}
	})
}

// A notifier installed before credentials were sealed holds its secret in
// clear under notify.secret/<name>: the release keeps it working for its
// signed calls until a rotation seals one.
func TestLegacyNotifierSecretStillVerifies(t *testing.T) {
	e, _, _ := notifyGW(t)
	e.gw.SecretBox = testSecretBox(t)
	e.gw.NotifierReplies = store.NewMemoryNotificationReplies()
	const legacy = "whsec_00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if err := e.gw.PlatformSettings.PutPlatformSetting(t.Context(), store.PlatformSetting{Key: "notify.secret/n1", Value: legacy}); err != nil {
		t.Fatal(err)
	}
	if code, raw := askSettings(t, e.srv.URL, "notifier/n1", legacy, time.Now()); code != http.StatusOK {
		t.Fatalf("the legacy secret's ask = %d %s", code, raw)
	}
	body := []byte(`{"replyToken":"rt_unknown","text":"hi","idempotencyKey":"k1"}`)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+wire.NotifierRepliesPath, bytes.NewReader(body))
	req.Header.Set(wire.ComponentHeader, "n1")
	req.Header.Set(wire.NotificationSignatureHeader, notify.Sign(legacy, time.Now(), body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Past the signature: the token is unknown, not the caller.
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the legacy secret's reply = %d, want 404 (an unknown token)", resp.StatusCode)
	}
}
