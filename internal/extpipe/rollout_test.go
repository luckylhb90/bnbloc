package extpipe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func TestPipelineInstallPersistsAutoRollout_whenPackageHasNoExplicitRollout(t *testing.T) {
	// Given
	p := newTestPipeline()
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(fakeKind, "ext-rollout-default") })

	// When
	rec, err := p.Install(ctx, InstallRequest{Body: fakePkg("ext-rollout-default"), Actor: "admin"})

	// Then
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec.Rollout != RolloutAuto {
		t.Fatalf("installed rollout = %q; want %q", rec.Rollout, RolloutAuto)
	}
	stored, ok, err := p.Settings.GetPlatformSetting(ctx, recordKey(fakeKind, "ext-rollout-default"))
	if err != nil || !ok {
		t.Fatalf("stored record = %+v,%v,%v; want persisted record", stored, ok, err)
	}
	var storedRollout struct {
		Rollout string `json:"rollout"`
	}
	if err := json.Unmarshal([]byte(stored.Value), &storedRollout); err != nil {
		t.Fatalf("decode stored record: %v", err)
	}
	if storedRollout.Rollout != RolloutAuto {
		t.Fatalf("stored rollout = %q; want %q", storedRollout.Rollout, RolloutAuto)
	}
}

func TestPipelineInstallPersistsRequestedRollout_whenRolloutIsOptInOrOff(t *testing.T) {
	tests := []struct {
		name    string
		rollout string
	}{
		{name: "optin", rollout: RolloutOptIn},
		{name: "off", rollout: RolloutOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			p := newTestPipeline()
			ctx := context.Background()
			name := "ext-rollout-" + tt.name
			t.Cleanup(func() { _ = extreg.Unregister(fakeKind, name) })

			// When
			rec, err := p.Install(ctx, InstallRequest{Body: fakePkg(name), Actor: "admin", Rollout: tt.rollout})

			// Then
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			if rec.Rollout != tt.rollout {
				t.Fatalf("installed rollout = %q; want %q", rec.Rollout, tt.rollout)
			}
			_, active := extreg.Get(fakeKind, name)
			if active != (tt.rollout != RolloutOff) {
				t.Fatalf("active = %v; want %v for rollout %q", active, tt.rollout != RolloutOff, tt.rollout)
			}
			stored, ok, err := p.Settings.GetPlatformSetting(ctx, recordKey(fakeKind, name))
			if err != nil || !ok {
				t.Fatalf("stored record = %+v,%v,%v; want persisted record", stored, ok, err)
			}
			var storedRollout struct {
				Rollout string `json:"rollout"`
			}
			if err := json.Unmarshal([]byte(stored.Value), &storedRollout); err != nil {
				t.Fatalf("decode stored record: %v", err)
			}
			if storedRollout.Rollout != tt.rollout {
				t.Fatalf("stored rollout = %q; want %q", storedRollout.Rollout, tt.rollout)
			}
		})
	}
}

func TestPipelineInstallRejectsInvalidRollout_beforePersistence(t *testing.T) {
	// Given
	p := newTestPipeline()
	ctx := context.Background()

	// When
	_, err := p.Install(ctx, InstallRequest{Body: fakePkg("ext-rollout-reject"), Actor: "admin", Rollout: "sideways"})

	// Then
	if err == nil || !strings.Contains(err.Error(), `invalid rollout "sideways"`) {
		t.Fatalf("install error = %v; want invalid rollout", err)
	}
	if stored, ok, err := p.Settings.GetPlatformSetting(ctx, recordKey(fakeKind, "ext-rollout-reject")); err != nil || ok {
		t.Fatalf("stored record = %+v,%v,%v; want no persisted record", stored, ok, err)
	}
}

func TestPipelineInstallLeavesNoRecord_whenActivationFails(t *testing.T) {
	// Given
	const failKind extreg.Kind = "fakeactivationfailkind"
	extreg.RegisterKind(extreg.KindDescriptor{Kind: failKind, Hosts: []extreg.Service{extreg.ServiceGateway}})
	pkgmeta.RegisterKindValidator(string(failKind), func(pkgmeta.Package) error { return nil })
	p := newTestPipeline()
	p.Installers = map[extreg.Kind]KindInstaller{
		failKind: {
			Kind: failKind,
			Decode: func(pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
				return json.RawMessage(`{}`), extreg.BackendNative, nil
			},
			Probe: func(context.Context, Record, extgov.Guard) error { return nil },
			Activate: func(context.Context, Record, extgov.Guard) (any, error) {
				return nil, errors.New("activation failed")
			},
		},
	}
	ctx := context.Background()
	body, err := json.Marshal(pkgmeta.Package{Kind: string(failKind), Name: "ext-activation-failure", Version: "1.0.0", Publisher: "test"})
	if err != nil {
		t.Fatalf("marshal package: %v", err)
	}

	// When
	_, err = p.Install(ctx, InstallRequest{Body: body, Actor: "admin", Rollout: RolloutOptIn})

	// Then
	if err == nil || !strings.Contains(err.Error(), "activation failed") {
		t.Fatalf("install error = %v; want activation failure", err)
	}
	if stored, ok, err := p.Settings.GetPlatformSetting(ctx, recordKey(failKind, "ext-activation-failure")); err != nil || ok {
		t.Fatalf("stored record = %+v,%v,%v; want no persisted record", stored, ok, err)
	}
	if _, ok := extreg.Get(failKind, "ext-activation-failure"); ok {
		t.Fatal("failed activation must not leave a registered extension")
	}
}

func TestPipelineRehydrateSkipsEnabledRecord_whenRolloutOff(t *testing.T) {
	// Given
	p := newTestPipeline()
	ctx := context.Background()
	if _, err := p.Install(ctx, InstallRequest{Body: fakePkg("ext-rollout-off-rehydrate"), Actor: "admin", Rollout: RolloutOff}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, ok := extreg.Get(fakeKind, "ext-rollout-off-rehydrate"); ok {
		t.Fatal("off rollout must not activate during install setup")
	}
	restarted := newTestPipeline()
	restarted.Settings = p.Settings
	restarted.Audit = p.Audit

	// When
	if err := restarted.Rehydrate(ctx, extreg.ServiceGateway); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}

	// Then
	if _, ok := extreg.Get(fakeKind, "ext-rollout-off-rehydrate"); ok {
		t.Fatal("off rollout must not activate during rehydrate")
	}
}

func TestPipelineListNormalizesLegacyEmptyRollout(t *testing.T) {
	// Given
	p := newTestPipeline()
	ctx := context.Background()
	legacy := Record{Kind: string(fakeKind), Name: "legacy", Backend: string(extreg.BackendNative)}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy record: %v", err)
	}
	if err := p.Settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: recordKey(fakeKind, "legacy"), Value: string(raw)}); err != nil {
		t.Fatalf("put legacy record: %v", err)
	}

	// When
	list, err := p.List(ctx, fakeKind)

	// Then
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Rollout != RolloutAuto {
		t.Fatalf("list = %+v; want legacy rollout normalized to %q", list, RolloutAuto)
	}
}

func TestPipelineListReturnsError_whenPersistedRolloutInvalid(t *testing.T) {
	// Given
	p := newTestPipeline()
	ctx := context.Background()
	key := recordKey(extreg.Kind("core"), "bad-rollout")
	if err := p.Settings.PutPlatformSetting(ctx, store.PlatformSetting{
		Key:   key,
		Value: `{"kind":"core","name":"bad-rollout","rollout":"sideways"}`,
	}); err != nil {
		t.Fatalf("put invalid record: %v", err)
	}

	// When
	_, err := p.List(ctx, extreg.Kind("core"))

	// Then
	if err == nil {
		t.Fatal("List returned nil error; want invalid persisted record error")
	}
	for _, want := range []string{key, "core/bad-rollout", `invalid rollout "sideways"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("List error = %v; want it to contain %q", err, want)
		}
	}
}

func TestPipelineSetRolloutPersistsAndAuditsTransition(t *testing.T) {
	// Given
	p := newTestPipeline()
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(fakeKind, "ext-rollout-transition") })
	if _, err := p.Install(ctx, InstallRequest{Body: fakePkg("ext-rollout-transition"), Actor: "admin"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	// When
	if err := p.SetRollout(ctx, fakeKind, "ext-rollout-transition", RolloutOptIn, "admin"); err != nil {
		t.Fatalf("set rollout: %v", err)
	}

	// Then
	list, err := p.List(ctx, fakeKind)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found Record
	for _, rec := range list {
		if rec.Name == "ext-rollout-transition" {
			found = rec
		}
	}
	if found.Rollout != RolloutOptIn {
		t.Fatalf("rollout = %q; want %q", found.Rollout, RolloutOptIn)
	}
	audits, err := p.Audit.ExtAuditRecords(ctx, string(fakeKind), 0, 0)
	if err != nil {
		t.Fatalf("audit records: %v", err)
	}
	if len(audits) == 0 || audits[0].Action != "set-rollout" || audits[0].Outcome != "allow" || audits[0].Detail != RolloutOptIn {
		t.Fatalf("latest audit = %+v; want set-rollout allow detail=%q", audits, RolloutOptIn)
	}
}

func TestPipelineSetRolloutRejectsInvalidPolicy(t *testing.T) {
	// Given
	p := newTestPipeline()
	ctx := context.Background()
	t.Cleanup(func() { _ = extreg.Unregister(fakeKind, "ext-rollout-invalid") })
	if _, err := p.Install(ctx, InstallRequest{Body: fakePkg("ext-rollout-invalid"), Actor: "admin"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	// When
	err := p.SetRollout(ctx, fakeKind, "ext-rollout-invalid", "sideways", "admin")

	// Then
	if err == nil || !strings.Contains(err.Error(), `invalid rollout "sideways"`) {
		t.Fatalf("SetRollout error = %v; want invalid rollout", err)
	}
	list, err := p.List(ctx, fakeKind)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rec := range list {
		if rec.Name == "ext-rollout-invalid" && rec.Rollout != RolloutAuto {
			t.Fatalf("rollout after invalid transition = %q; want %q", rec.Rollout, RolloutAuto)
		}
	}
}
