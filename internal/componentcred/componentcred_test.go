package componentcred

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func testBox(t *testing.T) *sealbox.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := sealbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// A credential is created once, sealed at rest bound to its key, rotated
// and deleted; every endpoint kind holds one and no other kind does.
func TestCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	settings := store.NewMemoryPlatformSettings()
	st := &Store{Settings: settings, Box: testBox(t)}
	for _, kind := range []string{"notifier", "auth-provider"} {
		secret, created, err := st.Ensure(ctx, kind, "corp", "root")
		if err != nil || !created || !strings.HasPrefix(secret, "whsec_") || len(secret) != len("whsec_")+64 {
			t.Fatalf("%s: ensure = %q, %v, %v", kind, secret, created, err)
		}
		again, created, err := st.Ensure(ctx, kind, "corp", "root")
		if err != nil || created || again != secret {
			t.Fatalf("%s: second ensure = %q, %v, %v (want the first, not created)", kind, again, created, err)
		}
		row, ok, err := settings.GetPlatformSetting(ctx, Key(kind, "corp"))
		if err != nil || !ok || strings.Contains(row.Value, secret) || strings.Contains(row.Value, "whsec_") || !strings.HasPrefix(row.Value, "v1:") {
			t.Fatalf("%s: stored row = %+v, %v (want sealed)", kind, row, err)
		}
		if row.UpdatedBy != "root" {
			t.Fatalf("%s: updated by %q", kind, row.UpdatedBy)
		}
		// A sealed value copied to another component's key does not open.
		if err := settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: Key(kind, "other"), Value: row.Value}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (&Store{Settings: settings, Box: st.Box}).Get(ctx, kind, "other"); err == nil {
			t.Fatalf("%s: a moved credential opened", kind)
		}
		rotated, err := st.Rotate(ctx, kind, "corp", "root")
		if err != nil || rotated == secret {
			t.Fatalf("%s: rotate = %q, %v", kind, rotated, err)
		}
		if got, ok, err := st.Get(ctx, kind, "corp"); err != nil || !ok || got != rotated {
			t.Fatalf("%s: get after rotate = %q, %v, %v", kind, got, ok, err)
		}
		// Another process (a fresh store) reads the same credential.
		if got, ok, err := (&Store{Settings: settings, Box: st.Box}).Get(ctx, kind, "corp"); err != nil || !ok || got != rotated {
			t.Fatalf("%s: another reader = %q, %v, %v", kind, got, ok, err)
		}
		if err := st.Delete(ctx, kind, "corp"); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.Get(ctx, kind, "corp"); err != nil || ok {
			t.Fatalf("%s: get after delete = %v, %v", kind, ok, err)
		}
	}
	for _, kind := range []string{"plugin", "mcp", "core", "previewer", "sandbox"} {
		if Holds(kind) {
			t.Errorf("%s holds a credential", kind)
		}
		if _, err := st.Rotate(ctx, kind, "corp", "root"); err == nil {
			t.Errorf("%s: rotate stored a credential", kind)
		}
	}
}

// The kinds holding a credential are the package kinds that ask for their
// settings (pkgmeta.AsksForSettings): the ask is signed with it.
func TestCredentialKindsAreTheAskingKinds(t *testing.T) {
	for kind, pkgKind := range map[string]string{
		"notifier": pkgmeta.KindNotifierPackageV1, "auth-provider": pkgmeta.KindAuthProviderPackageV1,
		"share-target": pkgmeta.KindShareTargetPackageV1,
		"plugin":       pkgmeta.KindPluginPackageV1, "mcp": pkgmeta.KindMCPPackageV1, "core": pkgmeta.KindCorePackageV1,
		"previewer": pkgmeta.KindPreviewerPackageV1, "sandbox": pkgmeta.KindSandboxPackageV1,
	} {
		if Holds(kind) != pkgmeta.AsksForSettings(pkgKind) {
			t.Errorf("%s: Holds = %v, AsksForSettings(%s) = %v", kind, Holds(kind), pkgKind, pkgmeta.AsksForSettings(pkgKind))
		}
	}
}

// Without the secret store key no credential is created or opened: a
// sealed one answers ErrOff rather than a guess.
func TestCredentialNeedsTheKey(t *testing.T) {
	ctx := context.Background()
	settings := store.NewMemoryPlatformSettings()
	keyless := &Store{Settings: settings}
	if keyless.Available() {
		t.Fatal("a keyless store is available")
	}
	if _, _, err := keyless.Ensure(ctx, "auth-provider", "sso", "root"); !errors.Is(err, ErrOff) {
		t.Fatalf("ensure without the key = %v, want ErrOff", err)
	}
	if _, err := (&Store{Settings: settings, Box: testBox(t)}).Rotate(ctx, "auth-provider", "sso", "root"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Store{Settings: settings}).Get(ctx, "auth-provider", "sso"); !errors.Is(err, ErrOff) {
		t.Fatalf("get of a sealed credential without the key = %v, want ErrOff", err)
	}
}

// A notifier installed before credentials were sealed kept its signing
// secret in clear under notify.secret/<name>: it keeps verifying (with or
// without the key, so a realtime not yet given the key still delivers),
// an install does not replace it, and the first rotation or the uninstall
// removes it.
func TestLegacyNotifierSecretKeepsWorking(t *testing.T) {
	ctx := context.Background()
	settings := store.NewMemoryPlatformSettings()
	const legacy = "whsec_00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	put := func() {
		t.Helper()
		if err := settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: "notify.secret/corp", Value: legacy, UpdatedBy: "root"}); err != nil {
			t.Fatal(err)
		}
	}
	put()
	box := testBox(t)
	for name, st := range map[string]*Store{"with the key": {Settings: settings, Box: box}, "without it": {Settings: settings}} {
		if got, ok, err := st.Get(ctx, "notifier", "corp"); err != nil || !ok || got != legacy {
			t.Fatalf("%s: get = %q, %v, %v", name, got, ok, err)
		}
	}
	// Only a notifier ever had one.
	if _, ok, _ := (&Store{Settings: settings, Box: box}).Get(ctx, "auth-provider", "corp"); ok {
		t.Fatal("an auth provider read the notifier's legacy secret")
	}
	st := &Store{Settings: settings, Box: box}
	if got, created, err := st.Ensure(ctx, "notifier", "corp", "root"); err != nil || created || got != legacy {
		t.Fatalf("an upgrade's ensure = %q, %v, %v (want the legacy secret kept)", got, created, err)
	}
	rotated, err := st.Rotate(ctx, "notifier", "corp", "root")
	if err != nil || rotated == legacy {
		t.Fatalf("rotate = %q, %v", rotated, err)
	}
	if _, ok, _ := settings.GetPlatformSetting(ctx, "notify.secret/corp"); ok {
		t.Fatal("the legacy row outlived the rotation")
	}
	if got, _, _ := st.Get(ctx, "notifier", "corp"); got != rotated {
		t.Fatalf("get after rotate = %q", got)
	}
	put()
	if err := st.Delete(ctx, "notifier", "corp"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := settings.GetPlatformSetting(ctx, "notify.secret/corp"); ok {
		t.Fatal("the legacy row outlived the uninstall")
	}
}
