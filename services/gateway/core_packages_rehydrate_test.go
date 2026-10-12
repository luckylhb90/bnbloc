package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/coreset"
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
      "messages": {"method":"GET","path":"/m","items":{"list":"messages","fields":{"text":"text"}}},
      "pollTurn": {"method":"GET","path":"/turn"}
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

	// The per-core env override means the same for an installed core as for
	// an env-configured one (coreset.EntryFor builds both).
	t.Setenv("BNBLOC_CORE_REHYDRATED_URL", "http://127.0.0.1:7777")
	t.Setenv("BNBLOC_CORE_REHYDRATED_TOKEN", "tok")

	RehydrateInstalledComponents(ctx, pipe, extreg.ServiceGateway)
	got := svc.coreByName("rehydrated")
	if got == nil {
		t.Fatal("rehydrated core was not reflected into service Cores")
	}
	if !got.Explicit || got.Endpoint.BaseURL != "http://127.0.0.1:7777" || got.Endpoint.Token != "tok" {
		t.Fatalf("installed core entry = %+v; want the BNBLOC_CORE_<NAME>_URL/_TOKEN override, explicit", got)
	}
	if _, err := core.New("rehydrated"); err != nil {
		t.Fatalf("rehydrated core not constructible: %v", err)
	}

	// Env-configured cores go first; an installed name keeps its activated
	// entry instead of a second one.
	svc.AddEnvCores([]coreset.Entry{{Name: "acme"}, {Name: "rehydrated"}})
	if len(svc.Cores) != 2 || svc.Cores[0].Name != "acme" || svc.Cores[1].Name != "rehydrated" || !svc.Cores[1].Explicit {
		t.Fatalf("cores after env = %+v; want [acme, activated rehydrated]", svc.Cores)
	}
}

func TestRehydrateManifestCoresNilStore(t *testing.T) {
	svc := &Service{}
	RehydrateInstalledComponents(context.Background(), &extpipe.Pipeline{}, extreg.ServiceGateway)
	if len(svc.Cores) != 0 {
		t.Fatalf("nil store activated cores: %+v", svc.Cores)
	}
}
