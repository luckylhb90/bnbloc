package componentsettings

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// An endpoint component asking for its settings is answered its stored
// platform values, the organization values of the organizations its
// install serves, the required ones not set per scope, and a version that
// changes exactly when the answer does.
func TestEndpointAnswer(t *testing.T) {
	ctx := context.Background()
	s := &Store{Settings: store.NewMemoryPlatformSettings(), Box: testBox(t)}
	decl := []pkgmeta.Setting{
		{Key: "metadataUrl", Scope: pkgmeta.SettingScopePlatform, Required: true},
		{Key: "spKey", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopePlatform},
		{Key: "lookup", Scope: pkgmeta.SettingScopePlatform, Default: "true"},
		{Key: "botId", Scope: pkgmeta.SettingScopeOrg, Required: true},
	}
	serves := func(tenant string) bool { return tenant != "t3" }
	update := func(kind, scope, tenant string, values map[string]string) {
		t.Helper()
		if _, err := s.Update(ctx, scope, tenant, kind, "sso", "root", func(map[string]string) (map[string]string, error) { return values, nil }); err != nil {
			t.Fatal(err)
		}
	}
	empty, err := s.Endpoint(ctx, "auth-provider", "sso", decl, serves)
	if err != nil || len(empty.Platform) != 0 || len(empty.Orgs) != 0 || !reflect.DeepEqual(empty.Missing.Platform, []string{"metadataUrl"}) || empty.Version == "" {
		t.Fatalf("nothing stored = %+v, %v", empty, err)
	}
	update("auth-provider", pkgmeta.SettingScopePlatform, "", map[string]string{"metadataUrl": "https://idp.example.com/meta", "spKey": "PEM", "stray": "x"})
	update("auth-provider", pkgmeta.SettingScopeOrg, "t1", map[string]string{"botId": "+1"})
	update("auth-provider", pkgmeta.SettingScopeOrg, "t2", map[string]string{})
	update("auth-provider", pkgmeta.SettingScopeOrg, "t3", map[string]string{"botId": "+3"})
	// Another kind's component of the same name is not this one.
	update("notifier", pkgmeta.SettingScopeOrg, "t4", map[string]string{"botId": "+4"})
	got, err := s.Endpoint(ctx, "auth-provider", "sso", decl, serves)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Platform, map[string]string{"metadataUrl": "https://idp.example.com/meta", "spKey": "PEM"}) {
		t.Fatalf("platform = %v (secrets included, undeclared dropped, no default filled)", got.Platform)
	}
	if !reflect.DeepEqual(got.Orgs, map[string]map[string]string{"t1": {"botId": "+1"}, "t2": {}}) {
		t.Fatalf("orgs = %v (the served organizations with a record)", got.Orgs)
	}
	if len(got.Missing.Platform) != 0 || !reflect.DeepEqual(got.Missing.Orgs, map[string][]string{"t1": {}, "t2": {"botId"}}) {
		t.Fatalf("missing = %+v", got.Missing)
	}
	if got.Version == empty.Version {
		t.Fatal("the version did not change with the values")
	}
	if again, _ := s.Endpoint(ctx, "auth-provider", "sso", decl, serves); again.Version != got.Version {
		t.Fatal("the same answer hashed differently")
	}
	update("auth-provider", pkgmeta.SettingScopeOrg, "t2", map[string]string{"botId": "+2"})
	if changed, _ := s.Endpoint(ctx, "auth-provider", "sso", decl, serves); changed.Version == got.Version {
		t.Fatal("an organization's change kept the version")
	}
	if _, err := (&Store{Settings: store.NewMemoryPlatformSettings()}).Endpoint(ctx, "auth-provider", "sso", decl, serves); !errors.Is(err, ErrOff) {
		t.Fatalf("without the key = %v, want ErrOff", err)
	}
}
