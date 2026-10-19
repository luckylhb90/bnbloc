package pkgmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func validPluginPackageBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"kind":         "bnbloc.plugin.package/v1",
		"name":         "acme-auditor",
		"version":      "0.3.1",
		"runtime":      "sidecar",
		"contract":     "bnbloc.plugin.contract/v1",
		"hooks":        []string{"chat_message.v1", "agent_messages.v1"},
		"artifacts":    map[string]any{"darwin-arm64": map[string]any{"url": "https://cdn.example/auditor-darwin.tar.gz", "sha256": strings.Repeat("a", 64)}},
		"sidecar":      map[string]any{"listenEnv": "AUDITOR_LISTEN_URL", "supervisedEnv": "AUDITOR_SUPERVISED", "localUrl": "http://127.0.0.1:7191", "args": []string{"--serve"}},
		"capabilities": []string{"room-events"},
		"permissions":  map[string]any{"network": []string{"cdn.example"}},
		"publisher":    "Acme Corp",
		"description":  "audits room events out of process",
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal plugin package: %v", err)
	}
	return raw
}

func TestParsePackage_parses_whenPluginPackageIsValid(t *testing.T) {
	// Given a well-formed plugin package declaring two known hooks.
	body := validPluginPackageBody(t, nil)

	// When
	pkg, err := ParsePackage(body)

	// Then
	if err != nil {
		t.Fatalf("parse plugin package: %v", err)
	}
	if pkg.Kind != KindPluginPackageV1 {
		t.Fatalf("kind = %q, want %q", pkg.Kind, KindPluginPackageV1)
	}
	if len(pkg.Hooks) != 2 || pkg.Hooks[0] != "chat_message.v1" || pkg.Hooks[1] != "agent_messages.v1" {
		t.Fatalf("hooks = %+v, want [chat_message.v1 agent_messages.v1]", pkg.Hooks)
	}
	if pkg.Contract != PluginContractV1 {
		t.Fatalf("contract = %q, want %q", pkg.Contract, PluginContractV1)
	}
}

func validAuthProviderPackageBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"kind":        KindAuthProviderPackageV1,
		"name":        "sso-provider",
		"version":     "0.1.0",
		"runtime":     "endpoint",
		"contract":    AuthProviderContractV1,
		"endpoint":    map[string]any{"baseUrl": "https://auth.internal.example"},
		"permissions": map[string]any{"network": []string{"auth.internal.example"}},
		"publisher":   "Acme Corp",
		"description": "auth provider endpoint",
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal auth-provider package: %v", err)
	}
	return raw
}

func TestParsePackage_parses_whenAuthProviderPackageIsValid(t *testing.T) {
	// Given an auth-provider package that exposes an externally supervised endpoint.
	body := validAuthProviderPackageBody(t, nil)

	// When
	pkg, err := ParsePackage(body)

	// Then
	if err != nil {
		t.Fatalf("parse auth-provider package: %v", err)
	}
	if pkg.Kind != KindAuthProviderPackageV1 {
		t.Fatalf("kind = %q, want %q", pkg.Kind, KindAuthProviderPackageV1)
	}
	if pkg.Contract != AuthProviderContractV1 {
		t.Fatalf("contract = %q, want %q", pkg.Contract, AuthProviderContractV1)
	}
	if pkg.Endpoint == nil || pkg.Endpoint.BaseURL != "https://auth.internal.example" {
		t.Fatalf("endpoint = %+v, want auth-provider endpoint", pkg.Endpoint)
	}
}

func TestParsePackage_refuses_whenAuthProviderContractIsUnknown(t *testing.T) {
	// Given an auth-provider package naming a contract the generic host does not implement.
	body := validAuthProviderPackageBody(t, func(b map[string]any) { b["contract"] = "bnbloc.auth-provider.contract/v9" })

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), `unknown auth-provider contract "bnbloc.auth-provider.contract/v9"`) {
		t.Fatalf("error = %v, want unknown auth-provider contract", err)
	}
}

func TestParsePackage_refuses_whenPluginRuntimeIsNotSidecar(t *testing.T) {
	// Given a plugin package whose runtime is endpoint (not an out-of-process sidecar).
	body := validPluginPackageBody(t, func(b map[string]any) { b["runtime"] = "endpoint" })

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), `plugin runtime must be "sidecar"`) {
		t.Fatalf("error = %v, want plugin runtime must be sidecar", err)
	}
}

func TestParsePackage_refuses_whenPluginDeclaresUnknownHook(t *testing.T) {
	// Given a plugin package declaring a hook the registry does not own.
	body := validPluginPackageBody(t, func(b map[string]any) {
		b["hooks"] = []string{"chat_message.v1", "presence_typing.v1"}
	})

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), `unknown hook "presence_typing.v1"`) {
		t.Fatalf("error = %v, want unknown hook presence_typing.v1", err)
	}
}

func TestParsePackage_refuses_whenPluginDeclaresNoHooks(t *testing.T) {
	// Given a plugin package with an empty hook list (it subscribes to nothing).
	body := validPluginPackageBody(t, func(b map[string]any) { b["hooks"] = []string{} })

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), "plugin must declare at least one hook") {
		t.Fatalf("error = %v, want at least one hook", err)
	}
}

func TestParsePackage_refuses_whenPluginContractIsUnknown(t *testing.T) {
	// Given a plugin package naming an unknown typed contract version.
	body := validPluginPackageBody(t, func(b map[string]any) { b["contract"] = "bnbloc.plugin.contract/v9" })

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), `unknown plugin contract "bnbloc.plugin.contract/v9"`) {
		t.Fatalf("error = %v, want unknown plugin contract", err)
	}
}

func TestParsePackage_refuses_whenPluginHasNoArtifacts(t *testing.T) {
	// Given a sidecar plugin with no platform artifacts to download.
	body := validPluginPackageBody(t, func(b map[string]any) { delete(b, "artifacts") })

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), "plugin sidecar requires artifacts") {
		t.Fatalf("error = %v, want plugin sidecar requires artifacts", err)
	}
}

func TestParsePackage_refuses_whenPluginArtifactHashIsMalformed(t *testing.T) {
	// Given a plugin artifact whose sha256 is not 64 lowercase hex characters.
	body := validPluginPackageBody(t, func(b map[string]any) {
		b["artifacts"] = map[string]any{"darwin-arm64": map[string]any{"url": "https://cdn.example/a.tar.gz", "sha256": "NOTAHASH"}}
	})

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), "sha256 must be 64 lowercase hex characters") {
		t.Fatalf("error = %v, want sha256 hex error", err)
	}
}

func TestParsePackage_refuses_whenPluginSidecarLocalUrlMissing(t *testing.T) {
	// Given a plugin that declares no loopback endpoint for its sidecar.
	body := validPluginPackageBody(t, func(b map[string]any) {
		b["sidecar"] = map[string]any{"listenEnv": "AUDITOR_LISTEN_URL"}
	})

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), "plugin sidecar requires sidecar.localUrl") {
		t.Fatalf("error = %v, want sidecar.localUrl required", err)
	}
}

func TestParsePackage_refuses_whenPluginNameMissing(t *testing.T) {
	// Given a plugin package with no name (a shared-shell required field).
	body := validPluginPackageBody(t, func(b map[string]any) { delete(b, "name") })

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), "package name required") {
		t.Fatalf("error = %v, want package name required", err)
	}
}

func TestParsePackage_refuses_whenKindIsUnknown(t *testing.T) {
	// Given a package naming a kind no validator owns.
	body := validPluginPackageBody(t, func(b map[string]any) { b["kind"] = "bnbloc.ghost.package/v1" })

	// When
	_, err := ParsePackage(body)

	// Then
	if err == nil || !strings.Contains(err.Error(), `unknown package kind "bnbloc.ghost.package/v1"`) {
		t.Fatalf("error = %v, want unknown package kind", err)
	}
}

func TestParsePackage_refuses_whenPluginDeclaresUnknownCapability(t *testing.T) {
	// Given a plugin package naming a capability outside the plugin registry.
	body := validPluginPackageBody(t, func(b map[string]any) {
		b["capabilities"] = []string{"room-events", "mintCoins"}
	})

	// When
	_, err := ParsePackage(body)

	// Then the shared capability gate rejects it for every kind.
	if err == nil || !strings.Contains(err.Error(), `unknown plugin capability "mintCoins"`) {
		t.Fatalf("error = %v, want unknown capability mintCoins", err)
	}
}

func TestParsePackage_accepts_whenPluginDeclaresPluginCapability(t *testing.T) {
	// Given a plugin package naming a plugin capability that is not a core protocol capability.
	body := validPluginPackageBody(t, func(b map[string]any) { b["capabilities"] = []string{"room-events"} })

	// When
	pkg, err := ParsePackage(body)

	// Then
	if err != nil {
		t.Fatalf("parse plugin package: %v", err)
	}
	if len(pkg.Capabilities) != 1 || pkg.Capabilities[0] != "room-events" {
		t.Fatalf("capabilities = %+v, want [room-events]", pkg.Capabilities)
	}
}

func TestHookRegistry_resolves_whenHookIsKnown(t *testing.T) {
	// Given the fixed first registry.
	// When
	spec, ok := LookupHook("chat_message.v1")

	// Then
	if !ok {
		t.Fatal("chat_message.v1 must be a registered hook")
	}
	if spec.Event != "chat_message" || spec.Version != "1" {
		t.Fatalf("spec = %+v, want event chat_message version 1", spec)
	}
}

func TestHookRegistry_lists_exactlyTheCodeOwnedHooks(t *testing.T) {
	// Given the fixed registry owned by code and tests.
	// When
	hooks := RegisteredHooks()

	// Then
	want := []string{"agent_messages.v1", "chat_message.v1", "notification.v1"}
	if strings.Join(hooks, ",") != strings.Join(want, ",") {
		t.Fatalf("registered hooks = %+v, want sorted %v", hooks, want)
	}
}

// ValidatePackage is the constructed-Package entry to the same gate
// ParsePackage applies (used by package-source tooling so it never keeps its
// own drifting copy); ValidatePackageShell covers only the kind-neutral shell.
func TestValidatePackage_matchesParseGate_andShellSkipsKind(t *testing.T) {
	shell := Package{Kind: "bnbloc.webhook.package/v1", Name: "hooks", Version: "0.1.0", Publisher: "p"}
	if err := ValidatePackageShell(shell); err != nil {
		t.Fatalf("shell of a well-formed package rejected: %v", err)
	}
	if err := ValidatePackage(shell); err == nil || !strings.Contains(err.Error(), "unknown package kind") {
		t.Fatalf("ValidatePackage = %v, want unknown kind (full gate)", err)
	}
	bad := shell
	bad.Version = "latest"
	if err := ValidatePackageShell(bad); err == nil || !strings.Contains(err.Error(), "semver") {
		t.Fatalf("ValidatePackageShell = %v, want semver failure", err)
	}
}

func TestParsePackage_acceptsNotificationPluginWithHostPermissionAndSettings(t *testing.T) {
	body := validPluginPackageBody(t, func(b map[string]any) {
		b["hooks"] = []string{NotificationHook}
		b["permissions"] = map[string]any{"network": []string{"cdn.example"}, "host": []string{PluginHostChat}}
		b["settings"] = []map[string]any{
			{"key": "apiBase", "label": "API base URL", "type": "url", "scope": "platform", "required": true, "deliver": map[string]any{"env": "BNBLOC_ACME_AUDITOR_API_BASE"}},
			{"key": "appId", "label": "App ID", "type": "text", "scope": "member", "required": true},
			{"key": "appKey", "label": "App key", "type": "secret", "scope": "member", "required": true, "help": "Stays on this computer."},
		}
	})
	pkg, err := ParsePackage(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pkg.Settings) != 3 || pkg.Settings[2].Type != SettingSecret || pkg.Permissions.Host[0] != PluginHostChat {
		t.Fatalf("pkg = %+v", pkg)
	}
	if spec, ok := LookupHook(NotificationHook); !ok || spec.Event != NotificationHookEvent || spec.Version != "1" {
		t.Fatalf("notification hook spec = %+v %v", spec, ok)
	}
}

func TestParsePackage_rejectsInvalidPluginHost(t *testing.T) {
	tests := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"unknown host permission": {func(b map[string]any) {
			b["permissions"] = map[string]any{"host": []string{"files"}}
		}, `unknown permissions.host entry "files"`},
		"duplicate host permission": {func(b map[string]any) {
			b["permissions"] = map[string]any{"host": []string{"chat", "chat"}}
		}, "duplicate permissions.host"},
		"localSettings is gone": {func(b map[string]any) {
			b["localSettings"] = []map[string]any{{"key": "a", "label": "A"}}
		}, "unknown field"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePackage(validPluginPackageBody(t, tt.mutate)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v; want %q", err, tt.want)
			}
		})
	}
}

func TestParsePackage_rejectsSettingsOnOtherKinds(t *testing.T) {
	body := validAuthProviderPackageBody(t, func(b map[string]any) {
		b["settings"] = []map[string]any{{"key": "a", "label": "A", "type": "text", "scope": "org"}}
	})
	if _, err := ParsePackage(body); err == nil || !strings.Contains(err.Error(), "must not declare settings") {
		t.Fatalf("error = %v; want settings refusal", err)
	}
}

func skillsPluginBody(t *testing.T, mutate func(map[string]any)) []byte {
	return validPluginPackageBody(t, func(b map[string]any) {
		b["hooks"] = []string{}
		b["capabilities"] = []string{PluginCapabilitySkillsProvider, PluginCapabilitySandboxMounts}
		b["sidecar"] = map[string]any{
			"localUrl": "http://127.0.0.1:7192", "listenEnv": "SK_LISTEN_URL", "tokenEnv": "SK_TOKEN", "dataDirEnv": "SK_DATA_DIR",
			"mountsEnv": "SK_MOUNTS_FILE", "mountRootEnv": "SK_MOUNT_ROOT",
		}
		if mutate != nil {
			mutate(b)
		}
	})
}

func TestParsePackage_pluginWithAnotherJobNeedsNoHook(t *testing.T) {
	pkg, err := ParsePackage(skillsPluginBody(t, nil))
	if err != nil {
		t.Fatalf("a skills plugin without hooks: %v", err)
	}
	if pkg.Sidecar.MountsEnv != "SK_MOUNTS_FILE" || pkg.Sidecar.MountRootEnv != "SK_MOUNT_ROOT" {
		t.Fatalf("mount envs survive: %+v", pkg.Sidecar)
	}
	cases := map[string]func(map[string]any){
		"requires sidecar.tokenEnv": func(b map[string]any) {
			sc := b["sidecar"].(map[string]any)
			delete(sc, "tokenEnv")
		},
		"requires sidecar.mountsEnv": func(b map[string]any) {
			delete(b["sidecar"].(map[string]any), "mountsEnv")
		},
		"need the sandbox-mounts capability": func(b map[string]any) {
			b["capabilities"] = []string{PluginCapabilitySkillsProvider}
		},
		"collides": func(b map[string]any) {
			b["sidecar"].(map[string]any)["mountRootEnv"] = "SK_MOUNTS_FILE"
		},
		"at least one hook": func(b map[string]any) {
			b["capabilities"] = []string{"room-events"}
			sc := b["sidecar"].(map[string]any)
			delete(sc, "mountsEnv")
			delete(sc, "mountRootEnv")
		},
	}
	for want, mutate := range cases {
		if _, err := ParsePackage(skillsPluginBody(t, mutate)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an error with %q, got %v", want, err)
		}
	}
}
