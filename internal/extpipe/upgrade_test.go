package extpipe

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// switchableKind is a kind whose probe/activate can be made to fail per
// version, so the tests can publish a good version and then a bad upgrade.
const switchableKind extreg.Kind = "fakeswitchablekind"

type switchable struct {
	failProbe    map[string]bool // version -> probe fails
	failActivate map[string]bool // version -> activate fails
	activations  []string        // versions activated, in order
	deactivated  []string
}

func newSwitchablePipeline(t *testing.T) (*Pipeline, *switchable) {
	t.Helper()
	sw := &switchable{failProbe: map[string]bool{}, failActivate: map[string]bool{}}
	p := newTestPipeline()
	p.Installers = map[extreg.Kind]KindInstaller{switchableKind: {
		Kind: switchableKind,
		Decode: func(pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
			return json.RawMessage(`{}`), extreg.BackendManifest, nil
		},
		Probe: func(_ context.Context, rec Record, _ extgov.Guard) error {
			if sw.failProbe[rec.Version] {
				return errors.New("probe failed for " + rec.Version)
			}
			return nil
		},
		Activate: func(_ context.Context, rec Record, _ extgov.Guard) (any, error) {
			if sw.failActivate[rec.Version] {
				return nil, errors.New("activate failed for " + rec.Version)
			}
			sw.activations = append(sw.activations, rec.Version)
			return "live:" + rec.Version, nil
		},
		Deactivate: func(_ context.Context, rec Record) error {
			sw.deactivated = append(sw.deactivated, rec.Version)
			return nil
		},
	}}
	return p, sw
}

func init() {
	extreg.RegisterKind(extreg.KindDescriptor{Kind: switchableKind, Hosts: []extreg.Service{extreg.ServiceGateway}})
	pkgmeta.RegisterKindValidator(string(switchableKind), func(pkgmeta.Package) error { return nil })
}

func switchablePkg(name, version string) []byte {
	b, _ := json.Marshal(pkgmeta.Package{Kind: string(switchableKind), Name: name, Version: version, Publisher: "test"})
	return b
}

func liveVersion(t *testing.T, name string) string {
	t.Helper()
	e, ok := extreg.Get(switchableKind, name)
	if !ok {
		return ""
	}
	return e.Instance.(string)
}

func TestInstallUpgradePreservesTenantsAndRollout(t *testing.T) {
	p, _ := newSwitchablePipeline(t)
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(switchableKind, "up") })
	if _, err := p.Install(ctx, InstallRequest{Body: switchablePkg("up", "1.0.0"), Actor: "admin", Rollout: RolloutOptIn}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := p.SetTenants(ctx, switchableKind, "up", []string{"t1"}, "admin"); err != nil {
		t.Fatalf("set tenants: %v", err)
	}

	rec, err := p.Install(ctx, InstallRequest{Body: switchablePkg("up", "2.0.0"), Actor: "admin"})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if rec.Version != "2.0.0" || rec.Rollout != RolloutOptIn || len(rec.EnabledTenants) != 1 || rec.EnabledTenants[0] != "t1" {
		t.Fatalf("upgraded record = %+v; want 2.0.0 keeping optin + [t1]", rec)
	}
	stored, _, _ := p.load(ctx, switchableKind, "up")
	if stored.Rollout != RolloutOptIn || !stored.AllowsTenant("t1") || stored.AllowsTenant("t2") {
		t.Fatalf("persisted upgrade = %+v; want optin + [t1]", stored)
	}

	// An explicit rollout on the upgrade request wins over the previous one.
	rec, err = p.Install(ctx, InstallRequest{Body: switchablePkg("up", "3.0.0"), Actor: "admin", Rollout: RolloutAuto})
	if err != nil || rec.Rollout != RolloutAuto || !rec.AllowsTenant("t1") || rec.AllowsTenant("t2") {
		t.Fatalf("explicit-rollout upgrade = %+v, %v; want auto keeping [t1]", rec, err)
	}
}

func TestInstallUpgradeProbeFailureKeepsPreviousRecord(t *testing.T) {
	p, sw := newSwitchablePipeline(t)
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(switchableKind, "probe-up") })
	if _, err := p.Install(ctx, InstallRequest{Body: switchablePkg("probe-up", "1.0.0"), Actor: "admin"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	sw.failProbe["2.0.0"] = true

	if _, err := p.Install(ctx, InstallRequest{Body: switchablePkg("probe-up", "2.0.0"), Actor: "admin"}); err == nil {
		t.Fatal("upgrade with failing probe succeeded")
	}
	stored, ok, _ := p.load(ctx, switchableKind, "probe-up")
	if !ok || stored.Version != "1.0.0" {
		t.Fatalf("stored after failed upgrade = %+v,%v; want the previous 1.0.0 record", stored, ok)
	}
	if got := liveVersion(t, "probe-up"); got != "live:1.0.0" {
		t.Fatalf("live driver = %q; want live:1.0.0", got)
	}
}

func TestInstallUpgradeActivationFailureRestoresPreviousRecord(t *testing.T) {
	p, sw := newSwitchablePipeline(t)
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(switchableKind, "act-up") })
	if _, err := p.Install(ctx, InstallRequest{Body: switchablePkg("act-up", "1.0.0"), Actor: "admin"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := p.SetTenants(ctx, switchableKind, "act-up", []string{"t9"}, "admin"); err != nil {
		t.Fatalf("set tenants: %v", err)
	}
	sw.failActivate["2.0.0"] = true

	_, err := p.Install(ctx, InstallRequest{Body: switchablePkg("act-up", "2.0.0"), Actor: "admin"})
	if err == nil || errors.Is(err, ErrPersist) {
		t.Fatalf("upgrade error = %v; want the activation failure (restore succeeded)", err)
	}
	stored, ok, _ := p.load(ctx, switchableKind, "act-up")
	if !ok || stored.Version != "1.0.0" || !stored.AllowsTenant("t9") || stored.AllowsTenant("t1") {
		t.Fatalf("stored after failed activation = %+v,%v; want previous 1.0.0 with [t9]", stored, ok)
	}
	if got := liveVersion(t, "act-up"); got != "live:1.0.0" {
		t.Fatalf("live driver = %q; want the previous version re-activated", got)
	}
}

func TestInstallUpgradeToOffDeactivatesPrevious(t *testing.T) {
	p, sw := newSwitchablePipeline(t)
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(switchableKind, "off-up") })
	if _, err := p.Install(ctx, InstallRequest{Body: switchablePkg("off-up", "1.0.0"), Actor: "admin"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := p.Install(ctx, InstallRequest{Body: switchablePkg("off-up", "2.0.0"), Actor: "admin", Rollout: RolloutOff}); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if got := liveVersion(t, "off-up"); got != "" {
		t.Fatalf("live driver = %q; want none for rollout off", got)
	}
	if len(sw.deactivated) != 1 || sw.deactivated[0] != "1.0.0" {
		t.Fatalf("deactivated = %v; want [1.0.0]", sw.deactivated)
	}
}

func TestRehydrateActivatesOptInRecords(t *testing.T) {
	p, _ := newSwitchablePipeline(t)
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(switchableKind, "optin-boot") })
	if _, err := p.Install(ctx, InstallRequest{Body: switchablePkg("optin-boot", "1.0.0"), Actor: "admin", Rollout: RolloutOptIn}); err != nil {
		t.Fatalf("install: %v", err)
	}
	_ = extreg.Unregister(switchableKind, "optin-boot") // simulate a restart

	if err := p.Rehydrate(ctx, extreg.ServiceGateway); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if got := liveVersion(t, "optin-boot"); got != "live:1.0.0" {
		t.Fatalf("live driver after rehydrate = %q; want the opt-in record active (gated per member at read time)", got)
	}
}
