package componentsettings

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func testBox(t *testing.T) *sealbox.Box {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	b, err := sealbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func put(t *testing.T, s *Store, scope, tenantID, name string, values map[string]string) {
	t.Helper()
	if _, err := s.Update(context.Background(), scope, tenantID, "mcp", name, "admin", func(map[string]string) (map[string]string, error) { return values, nil }); err != nil {
		t.Fatal(err)
	}
}

// Records are sealed per scope, organization and component: never in
// clear, never opened under another key, gone with their component.
func TestRecordsSealedPerScopeAndOrganization(t *testing.T) {
	ctx := context.Background()
	settings := store.NewMemoryPlatformSettings()
	s := &Store{Settings: settings, Box: testBox(t), Now: func() int64 { return 42 }}
	put(t, s, pkgmeta.SettingScopeOrg, "t1", "search", map[string]string{"token": "s3cr3t"})
	rows, _ := settings.ListPlatformSettings(ctx)
	if len(rows) != 1 || rows[0].Key != "component.settings/org/t1/mcp/search" || strings.Contains(rows[0].Value, "s3cr3t") {
		t.Fatalf("stored = %+v", rows)
	}
	got, ok, err := s.Get(ctx, pkgmeta.SettingScopeOrg, "t1", "mcp", "search")
	if err != nil || !ok || got.Values["token"] != "s3cr3t" || got.UpdatedBy != "admin" || got.UpdatedAtMs != 42 {
		t.Fatalf("get = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.Get(ctx, pkgmeta.SettingScopeOrg, "t2", "mcp", "search"); ok {
		t.Fatal("another organization has no record")
	}
	if _, ok, _ := s.Get(ctx, pkgmeta.SettingScopeOrg, "t1", "plugin", "search"); ok {
		t.Fatal("a component of another kind with the same name has no record")
	}
	// A row copied to another organization does not open.
	_ = settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: "component.settings/org/t2/mcp/search", Value: rows[0].Value})
	if _, _, err := s.Get(ctx, pkgmeta.SettingScopeOrg, "t2", "mcp", "search"); err == nil {
		t.Fatal("a moved sealed row must not open")
	}
	if _, err := Key(pkgmeta.SettingScopeMember, "t1", "mcp", "search"); err == nil {
		t.Fatal("member values are never stored on the server")
	}
	put(t, s, pkgmeta.SettingScopePlatform, "", "search", map[string]string{"region": "eu"})
	put(t, s, pkgmeta.SettingScopeOrg, "t1", "other", map[string]string{"x": "y"})
	if err := s.DeleteComponent(ctx, "mcp", "search"); err != nil {
		t.Fatal(err)
	}
	rows, _ = settings.ListPlatformSettings(ctx)
	if len(rows) != 1 || rows[0].Key != "component.settings/org/t1/mcp/other" {
		t.Fatalf("after uninstall = %+v", rows)
	}
	off := &Store{Settings: settings}
	if _, err := off.Update(ctx, pkgmeta.SettingScopeOrg, "t1", "mcp", "x", "a", nil); !errors.Is(err, ErrOff) {
		t.Fatalf("without a key settings are off: %v", err)
	}
}

// Two admins saving at once both land: a save is one read-modify-write.
func TestConcurrentSavesBothLand(t *testing.T) {
	s := &Store{Settings: store.NewMemoryPlatformSettings(), Box: testBox(t)}
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Update(context.Background(), pkgmeta.SettingScopeOrg, "t1", "mcp", "search", "admin", func(cur map[string]string) (map[string]string, error) {
				cur[key] = "v"
				return cur, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _, _ := s.Get(context.Background(), pkgmeta.SettingScopeOrg, "t1", "mcp", "search")
	if len(got.Values) != 4 {
		t.Fatalf("values = %v", got.Values)
	}
}

// Resolved answers the platform and organization values of the
// declaration's keys of those scopes, and the required ones not set.
func TestResolved(t *testing.T) {
	s := &Store{Settings: store.NewMemoryPlatformSettings(), Box: testBox(t)}
	decl := []pkgmeta.Setting{
		{Key: "apiBase", Scope: pkgmeta.SettingScopePlatform, Required: true},
		{Key: "apiKey", Scope: pkgmeta.SettingScopeOrg, Required: true},
		{Key: "region", Scope: pkgmeta.SettingScopeOrg, Required: true, Default: "eu"},
		{Key: "chrome", Scope: pkgmeta.SettingScopeMember, Required: true},
	}
	put(t, s, pkgmeta.SettingScopePlatform, "", "notes", map[string]string{"apiBase": "https://api.example.com", "stray": "x"})
	values, missing, err := s.Resolved(context.Background(), "t1", "mcp", "notes", decl)
	if err != nil || !reflect.DeepEqual(values, map[string]string{"apiBase": "https://api.example.com"}) || !reflect.DeepEqual(missing, []string{"apiKey"}) {
		t.Fatalf("resolved = %v %v %v", values, missing, err)
	}
	put(t, s, pkgmeta.SettingScopeOrg, "t1", "notes", map[string]string{"apiKey": "k"})
	values, missing, _ = s.Resolved(context.Background(), "t1", "mcp", "notes", decl)
	if values["apiKey"] != "k" || len(missing) != 0 {
		t.Fatalf("resolved = %v %v", values, missing)
	}
	if _, missing, _ := s.Resolved(context.Background(), "t2", "mcp", "notes", decl); !reflect.DeepEqual(missing, []string{"apiKey"}) {
		t.Fatalf("another organization's = %v", missing)
	}
	off := &Store{Settings: store.NewMemoryPlatformSettings()}
	if values, missing, err := off.Resolved(context.Background(), "t1", "mcp", "notes", decl); err != nil || len(values) != 0 || len(missing) != 2 {
		t.Fatalf("off = %v %v %v", values, missing, err)
	}
}
