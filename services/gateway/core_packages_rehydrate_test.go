package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func TestRehydrateManifestCoresFromStore(t *testing.T) {
	ctx := context.Background()
	settings := store.NewMemoryPlatformSettings()

	manifest, err := manifestcore.ParseManifest([]byte(`{
      "name": "rehydrated",
      "createWorkspace": {"method":"POST","path":"/w"},
      "createSession": {"method":"POST","path":"/s"},
      "sendInput": {"method":"POST","path":"/i"},
      "listTargets": {"method":"GET","path":"/t"},
      "sessionIdle": {"method":"GET","path":"/idle"},
      "latestSession": {"method":"GET","path":"/latest"}
    }`))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	pkg := pkgmeta.Package{
		Kind:      pkgmeta.KindCorePackageV1,
		Name:      manifest.Name,
		Version:   "1.0.0",
		Publisher: "test",
		Protocol:  protocol.ProtocolRange{Min: protocol.SupportedMinVersion, Max: protocol.SupportedMaxVersion},
		Runtime:   pkgmeta.RuntimeManifest,
		Manifest:  &manifest,
	}
	body, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal package: %v", err)
	}
	svc := &Service{PlatformSettings: settings, AllowExtUnsigned: true}
	pipe := svc.legacyManifestInstallPipeline()
	if _, err := pipe.Install(ctx, extpipe.InstallRequest{Body: body, Actor: "admin"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Cleanup(func() { _ = core.Unregister("rehydrated") })
	_ = core.Unregister("rehydrated")

	names := RehydrateInstalledComponents(ctx, pipe, extreg.ServiceGateway)
	if len(names) != 1 || names[0] != "rehydrated" {
		t.Fatalf("rehydrated names = %v; want [rehydrated]", names)
	}
	if svc.coreByName("rehydrated") == nil {
		t.Fatal("rehydrated core was not reflected into service Cores")
	}
	if _, err := core.New("rehydrated"); err != nil {
		t.Fatalf("rehydrated core not constructible: %v", err)
	}
}

func TestRehydrateManifestCoresNilStore(t *testing.T) {
	pipe := &extpipe.Pipeline{}
	if names := RehydrateInstalledComponents(context.Background(), pipe, extreg.ServiceGateway); names != nil {
		t.Fatalf("nil store = %v; want nil", names)
	}
}
