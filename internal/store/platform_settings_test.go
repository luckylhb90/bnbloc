package store

import (
	"context"
	"testing"
)

// Platform settings on both backends: a put replaces the value and its
// metadata, a missing key is found=false, the listing is by key, and a
// delete is idempotent.
func TestContractPlatformSettingsStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.Settings
		ctx := context.Background()
		p := uniq("ps.")
		if err := s.PutPlatformSetting(ctx, PlatformSetting{Key: p + ".updates", Value: "false", UpdatedAtMs: 10, UpdatedBy: "alice"}); err != nil {
			t.Fatalf("initial put: %v", err)
		}
		if err := s.PutPlatformSetting(ctx, PlatformSetting{Key: p + ".updates", Value: "true", UpdatedAtMs: 20, UpdatedBy: "root"}); err != nil {
			t.Fatalf("update put: %v", err)
		}
		got, ok, err := s.GetPlatformSetting(ctx, p+".updates")
		if err != nil || !ok || got.Value != "true" || got.UpdatedAtMs != 20 || got.UpdatedBy != "root" {
			t.Fatalf("setting = %+v %v %v, want the updated value and metadata", got, ok, err)
		}
		if _, ok, err := s.GetPlatformSetting(ctx, p+".missing"); err != nil || ok {
			t.Fatalf("missing = %v, %v", ok, err)
		}
		if err := s.PutPlatformSetting(ctx, PlatformSetting{Key: p + ".release", Value: "0.1.2"}); err != nil {
			t.Fatal(err)
		}
		var mine []string
		list := must(s.ListPlatformSettings(ctx))(t)
		for i, row := range list {
			if i > 0 && list[i-1].Key >= row.Key {
				t.Fatalf("listing out of key order at %d: %q then %q", i, list[i-1].Key, row.Key)
			}
			if len(row.Key) > len(p) && row.Key[:len(p)] == p {
				mine = append(mine, row.Key)
			}
		}
		if len(mine) != 2 || mine[0] != p+".release" || mine[1] != p+".updates" {
			t.Fatalf("settings = %v, want sorted keys", mine)
		}
		for i := 0; i < 2; i++ {
			if err := s.DeletePlatformSetting(ctx, p+".release"); err != nil {
				t.Fatalf("delete #%d: %v", i, err)
			}
		}
		if _, ok, _ := s.GetPlatformSetting(ctx, p+".release"); ok {
			t.Fatal("deleted setting still found")
		}
	})
}

// An empty listing is [] on both backends.
func TestContractPlatformSettingsStoreEmptyList(t *testing.T) {
	mem := NewMemoryPlatformSettings()
	if got := must(mem.ListPlatformSettings(context.Background()))(t); got == nil || len(got) != 0 {
		t.Fatalf("memory empty list = %#v", got)
	}
	forEachBackend(t, func(t *testing.T, b *Backends) {
		if got := must(b.Settings.ListPlatformSettings(context.Background()))(t); got == nil {
			t.Fatal("listing is nil, want a (possibly empty) slice")
		}
	})
}
