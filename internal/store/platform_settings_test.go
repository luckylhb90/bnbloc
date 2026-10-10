package store

import (
	"context"
	"testing"
)

func TestMemoryPlatformSettingsRoundTrip_whenSettingIsUpdated(t *testing.T) {
	settings := NewMemoryPlatformSettings()
	ctx := context.Background()

	if err := settings.PutPlatformSetting(ctx, PlatformSetting{Key: "updates.enabled", Value: "false", UpdatedAtMs: 10, UpdatedBy: "alice"}); err != nil {
		t.Fatalf("initial put: %v", err)
	}
	if err := settings.PutPlatformSetting(ctx, PlatformSetting{Key: "updates.enabled", Value: "true", UpdatedAtMs: 20, UpdatedBy: "root"}); err != nil {
		t.Fatalf("update put: %v", err)
	}

	got, ok, err := settings.GetPlatformSetting(ctx, "updates.enabled")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("expected setting to exist")
	}
	if got.Value != "true" || got.UpdatedAtMs != 20 || got.UpdatedBy != "root" {
		t.Fatalf("setting = %+v, want updated value metadata", got)
	}
}

func TestMemoryPlatformSettingsList_whenMultipleKeysExist(t *testing.T) {
	settings := NewMemoryPlatformSettings()
	ctx := context.Background()

	for _, setting := range []PlatformSetting{
		{Key: "release.version", Value: "0.1.2"},
		{Key: "updates.enabled", Value: "true"},
	} {
		if err := settings.PutPlatformSetting(ctx, setting); err != nil {
			t.Fatalf("put %s: %v", setting.Key, err)
		}
	}

	got, err := settings.ListPlatformSettings(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].Key != "release.version" || got[1].Key != "updates.enabled" {
		t.Fatalf("settings = %+v, want sorted keys", got)
	}
}
