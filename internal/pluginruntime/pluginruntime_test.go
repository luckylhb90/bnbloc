package pluginruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
)

func validPlugin(localURL string) Config {
	return Config{
		Runtime:     pkgmeta.RuntimeSidecar,
		Contract:    pkgmeta.PluginContractV1,
		Hooks:       []string{"chat_message.v1"},
		Sidecar:     &pkgmeta.Sidecar{LocalURL: localURL},
		Permissions: pkgmeta.Permissions{Network: []string{"127.0.0.1"}},
	}
}

func pluginRecord(t *testing.T, cfg Config) extpipe.Record {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return extpipe.Record{Kind: string(pkgmeta.KindPlugin), Name: "auditor", Config: raw, Network: cfg.Permissions.Network}
}

func TestProbeRecord_acceptsValidSidecarPlugin(t *testing.T) {
	cfg := validPlugin("http://127.0.0.1:7191")
	if err := ProbeRecord(context.Background(), pluginRecord(t, cfg), extgov.Guard{Net: extgov.NewNetGuard(cfg.Permissions.Network)}); err != nil {
		t.Fatalf("probe: %v", err)
	}
}

func TestProbeRecord_rejectsContractViolations(t *testing.T) {
	tests := map[string]func(*Config){
		"endpoint runtime":      func(c *Config) { c.Runtime = pkgmeta.RuntimeEndpoint },
		"unknown contract":      func(c *Config) { c.Contract = "bnbloc.plugin.contract/v0" },
		"unregistered hook":     func(c *Config) { c.Hooks = []string{"chat_message.v1", "room_deleted.v1"} },
		"missing sidecar":       func(c *Config) { c.Sidecar = nil },
		"blank localUrl":        func(c *Config) { c.Sidecar = &pkgmeta.Sidecar{LocalURL: " "} },
		"localUrl not loopback": func(c *Config) { c.Sidecar = &pkgmeta.Sidecar{LocalURL: "http://evil.example:7191"} },
		"localUrl not http":     func(c *Config) { c.Sidecar = &pkgmeta.Sidecar{LocalURL: "https://127.0.0.1:7191"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validPlugin("http://127.0.0.1:7191")
			mutate(&cfg)
			if err := ProbeRecord(context.Background(), pluginRecord(t, cfg), extgov.Guard{Net: extgov.NewNetGuard(cfg.Permissions.Network)}); err == nil {
				t.Fatal("invalid plugin accepted")
			}
		})
	}
}

func TestHasHook(t *testing.T) {
	cfg := validPlugin("http://127.0.0.1:7191")
	if !HasHook(cfg, "chat_message.v1") || HasHook(cfg, "agent_messages.v1") {
		t.Fatalf("HasHook mismatch for hooks %v", cfg.Hooks)
	}
}

func TestInstaller_activatesWithDecodedConfig(t *testing.T) {
	var got Config
	in := Installer(func(_ context.Context, _ extpipe.Record, cfg Config, _ extgov.Guard) (any, error) {
		got = cfg
		return "live", nil
	}, nil)
	cfg := validPlugin("http://127.0.0.1:7191")
	inst, err := in.Activate(context.Background(), pluginRecord(t, cfg), extgov.Guard{})
	if err != nil || inst != "live" {
		t.Fatalf("activate = %v, %v", inst, err)
	}
	if got.Sidecar == nil || got.Sidecar.LocalURL != "http://127.0.0.1:7191" || got.Hooks[0] != "chat_message.v1" {
		t.Fatalf("activate saw config %+v", got)
	}
	if _, err := in.Activate(context.Background(), extpipe.Record{Config: json.RawMessage(`{`)}, extgov.Guard{}); err == nil {
		t.Fatal("corrupt record activated")
	}
	// The default activate (gateway: plugins never run in-process) is a no-op.
	if inst, err := Installer(nil, nil).Activate(context.Background(), pluginRecord(t, cfg), extgov.Guard{}); inst != nil || err != nil {
		t.Fatalf("default activate = %v, %v; want nil, nil", inst, err)
	}
}

func TestDecodePackage_projectsPluginFields(t *testing.T) {
	pkg := pkgmeta.Package{
		Kind: pkgmeta.KindPluginPackageV1, Name: "auditor", Version: "0.1.0",
		Runtime: pkgmeta.RuntimeSidecar, Contract: pkgmeta.PluginContractV1,
		Hooks: []string{"agent_messages.v1"}, Sidecar: &pkgmeta.Sidecar{LocalURL: "http://127.0.0.1:7191"},
	}
	raw, _, err := DecodePackage(pkg)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeRecord(extpipe.Record{Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Contract != pkgmeta.PluginContractV1 || len(cfg.Hooks) != 1 || cfg.Hooks[0] != "agent_messages.v1" || cfg.Sidecar.LocalURL != "http://127.0.0.1:7191" {
		t.Fatalf("decoded = %+v", cfg)
	}
}
