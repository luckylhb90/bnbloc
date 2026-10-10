package extpipe

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// A fake kind proves the pipeline dispatches to a registered installer without
// knowing the kind's concrete contract.
const fakeKind extreg.Kind = "faketestkind"

func init() {
	extreg.RegisterKind(extreg.KindDescriptor{Kind: fakeKind, Hosts: []extreg.Service{extreg.ServiceGateway}})
	pkgmeta.RegisterKindValidator(string(fakeKind), func(pkgmeta.Package) error { return nil })
	RegisterInstaller(KindInstaller{
		Kind: fakeKind,
		Decode: func(pkg pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
			return json.RawMessage(`{}`), extreg.BackendNative, nil
		},
		Probe: func(context.Context, Record, extgov.Guard) error { return nil },
		Activate: func(_ context.Context, rec Record, _ extgov.Guard) (any, error) {
			return "live:" + rec.Name, nil
		},
	})
}

func newTestPipeline() *Pipeline {
	return &Pipeline{
		Settings:      store.NewMemoryPlatformSettings(),
		Audit:         store.NewMemoryExtensionAudit(),
		AllowUnsigned: true,
	}
}

func fakePkg(name string) []byte {
	b, _ := json.Marshal(pkgmeta.Package{Kind: string(fakeKind), Name: name, Version: "1.0.0", Publisher: "test"})
	return b
}

func TestPipelineInstallActivatesAndPersists(t *testing.T) {
	p := newTestPipeline()
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(fakeKind, "ext1") })

	rec, err := p.Install(ctx, InstallRequest{Body: fakePkg("ext1"), Actor: "admin"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec.Rollout != RolloutAuto {
		t.Fatalf("rollout = %q; want auto", rec.Rollout)
	}
	e, ok := extreg.Get(fakeKind, "ext1")
	if !ok || e.Instance.(string) != "live:ext1" || e.Source != extreg.SourceInstalled {
		t.Fatalf("registered = %+v; want live installed instance", e)
	}
	list, _ := p.List(ctx, fakeKind)
	if len(list) != 1 || list[0].Name != "ext1" {
		t.Fatalf("List = %+v; want [ext1]", list)
	}
}

func TestPipelineInstallDispatchesPackageKindToExtensionKind(t *testing.T) {
	const extKind extreg.Kind = "fakepackagekindtarget"
	const packageKind = "faketestkind.package/v1"
	extreg.RegisterKind(extreg.KindDescriptor{Kind: extKind, Hosts: []extreg.Service{extreg.ServiceGateway}})
	pkgmeta.RegisterKindValidator(packageKind, func(pkgmeta.Package) error { return nil })
	installer := KindInstaller{
		Kind:         extKind,
		PackageKinds: []string{packageKind},
		Decode: func(pkg pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
			return json.RawMessage(`{"packageKind":` + strconv.Quote(pkg.Kind) + `}`), extreg.BackendNative, nil
		},
		Probe: func(context.Context, Record, extgov.Guard) error { return nil },
		Activate: func(_ context.Context, rec Record, _ extgov.Guard) (any, error) {
			return rec.Kind + ":" + rec.Name, nil
		},
	}
	p := newTestPipeline()
	p.Installers = map[extreg.Kind]KindInstaller{extKind: installer}
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(extKind, "ext-package-kind") })
	body, _ := json.Marshal(pkgmeta.Package{Kind: packageKind, Name: "ext-package-kind", Version: "1.0.0", Publisher: "test"})

	rec, err := p.Install(ctx, InstallRequest{Body: body, Actor: "admin"})

	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec.Kind != string(extKind) {
		t.Fatalf("record kind = %q; want %q", rec.Kind, extKind)
	}
	if _, ok := extreg.Get(extKind, "ext-package-kind"); !ok {
		t.Fatal("extension was not registered under its extreg kind")
	}
}

func TestPipelineVerifyUsesConfiguredTrustedKeysKey(t *testing.T) {
	p := newTestPipeline()
	p.TrustedKeysKey = "custom.trustedKeys"
	ctx := context.Background()
	stored, _ := json.Marshal([]pkgmeta.TrustedKey{{KeyID: "custom", PublicKey: "pub"}})
	if err := p.Settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: p.TrustedKeysKey, Value: string(stored)}); err != nil {
		t.Fatalf("put custom trusted key: %v", err)
	}

	keys, err := p.trustedKeys(ctx)

	if err != nil {
		t.Fatalf("trusted keys: %v", err)
	}
	if len(keys) != 1 || keys[0].KeyID != "custom" {
		t.Fatalf("trusted keys = %+v; want custom key", keys)
	}
}

func TestPipelineUninstallRemoves(t *testing.T) {
	p := newTestPipeline()
	ctx := context.Background()
	if _, err := p.Install(ctx, InstallRequest{Body: fakePkg("ext2"), Actor: "admin"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := p.Uninstall(ctx, fakeKind, "ext2", "admin"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, ok := extreg.Get(fakeKind, "ext2"); ok {
		t.Fatal("uninstalled extension still registered")
	}
	list, _ := p.List(ctx, fakeKind)
	if len(list) != 0 {
		t.Fatalf("List after uninstall = %+v; want empty", list)
	}
}

func TestPipelineInstallUnsignedRefusedWhenNotAllowed(t *testing.T) {
	p := newTestPipeline()
	p.AllowUnsigned = false
	if _, err := p.Install(context.Background(), InstallRequest{Body: fakePkg("ext4"), Actor: "admin"}); err == nil {
		t.Fatal("unsigned install must be refused when AllowUnsigned is false")
	}
}

func TestPipelineSetTenantsPersistsOverlay(t *testing.T) {
	p := newTestPipeline()
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(fakeKind, "exttenants") })
	if _, err := p.Install(ctx, InstallRequest{Body: fakePkg("exttenants"), Actor: "admin"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := p.SetTenants(ctx, fakeKind, "exttenants", []string{"t1", "t2"}, "admin"); err != nil {
		t.Fatalf("set tenants: %v", err)
	}
	list, _ := p.List(ctx, fakeKind)
	found := false
	for _, r := range list {
		if r.Name == "exttenants" {
			found = true
			if len(r.EnabledTenants) != 2 || r.EnabledTenants[0] != "t1" {
				t.Fatalf("EnabledTenants = %+v; want [t1 t2]", r.EnabledTenants)
			}
		}
	}
	if !found {
		t.Fatal("record not found after SetTenants")
	}
	for _, empty := range [][]string{nil, {}, {" ", ""}} {
		if err := p.SetTenants(ctx, fakeKind, "exttenants", empty, "admin"); !errors.Is(err, ErrNoTenants) {
			t.Fatalf("SetTenants(%q) error = %v; want ErrNoTenants (an empty restriction must fail closed)", empty, err)
		}
	}
	list1, _ := p.List(ctx, fakeKind)
	for _, r := range list1 {
		if r.Name == "exttenants" && (len(r.EnabledTenants) != 2 || r.AllowsTenant("t3")) {
			t.Fatalf("rejected SetTenants changed the overlay: %+v", r.EnabledTenants)
		}
	}
	if err := p.ClearTenants(ctx, fakeKind, "exttenants", "admin"); err != nil {
		t.Fatalf("clear tenants: %v", err)
	}
	list2, _ := p.List(ctx, fakeKind)
	for _, r := range list2 {
		if r.Name == "exttenants" && (r.EnabledTenants != nil || !r.AllowsTenant("t3")) {
			t.Fatalf("cleared EnabledTenants = %+v; want nil (all tenants)", r.EnabledTenants)
		}
	}
}

func TestPipelineSetTenantsNormalizes(t *testing.T) {
	p := newTestPipeline()
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(fakeKind, "extnorm") })
	if _, err := p.Install(ctx, InstallRequest{Body: fakePkg("extnorm"), Actor: "admin"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := p.SetTenants(ctx, fakeKind, "extnorm", []string{" t2", "t1", "t2", ""}, "admin"); err != nil {
		t.Fatalf("set tenants: %v", err)
	}
	rec, _, _ := p.load(ctx, fakeKind, "extnorm")
	if strings.Join(rec.EnabledTenants, ",") != "t1,t2" {
		t.Fatalf("EnabledTenants = %q; want [t1 t2]", rec.EnabledTenants)
	}
}

func validPluginBody() []byte {
	b, _ := json.Marshal(pkgmeta.Package{
		Kind:        pkgmeta.KindPluginPackageV1,
		Name:        "acme-auditor",
		Version:     "0.3.1",
		Runtime:     pkgmeta.RuntimeSidecar,
		Contract:    pkgmeta.PluginContractV1,
		Hooks:       []string{"chat_message.v1"},
		Artifacts:   map[string]pkgmeta.Artifact{"darwin-arm64": {URL: "https://cdn.example/a.tar.gz", SHA256: strings.Repeat("a", 64)}},
		Sidecar:     &pkgmeta.Sidecar{ListenEnv: "AUDITOR_LISTEN_URL", LocalURL: "http://127.0.0.1:7191"},
		Permissions: pkgmeta.Permissions{Network: []string{"cdn.example"}},
		Publisher:   "Acme Corp",
	})
	return b
}

func TestPipelineInstallRejectsPluginKindWithoutInstaller(t *testing.T) {
	// Given a pipeline with only the core installer wired — Task 1 registers the
	// plugin kind descriptor but intentionally wires no plugin installer yet.
	p := newTestPipeline()
	ctx := context.Background()

	// When a valid plugin package is installed.
	_, err := p.Install(ctx, InstallRequest{Body: validPluginBody(), Actor: "admin"})

	// Then it is refused for having no installer, and nothing is persisted.
	if err == nil || !strings.Contains(err.Error(), `no installer registered for kind "bnbloc.plugin.package/v1"`) {
		t.Fatalf("error = %v, want no installer registered for the plugin kind", err)
	}
	if _, ok := extreg.Get(pkgmeta.KindPlugin, "acme-auditor"); ok {
		t.Fatal("a plugin with no installer must not be registered")
	}
	list, _ := p.List(ctx, pkgmeta.KindPlugin)
	if len(list) != 0 {
		t.Fatalf("persisted records = %+v, want none for an uninstallable plugin", list)
	}
}

func TestPipelineRegistersPluginKindDescriptor(t *testing.T) {
	// Given the pkgmeta init that registers the plugin kind.
	// When
	desc, ok := extreg.Descriptor(pkgmeta.KindPlugin)

	// Then the plugin kind is hosted by realtime (where hooks dispatch).
	if !ok {
		t.Fatal("plugin kind descriptor must be registered")
	}
	if len(desc.Hosts) != 1 || desc.Hosts[0] != extreg.ServiceRealtime {
		t.Fatalf("plugin hosts = %+v, want [realtime]", desc.Hosts)
	}
}
