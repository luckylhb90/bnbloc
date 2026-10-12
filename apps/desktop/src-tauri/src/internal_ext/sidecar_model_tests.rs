use std::collections::HashSet;

use super::*;

#[test]
fn resolve_component_specs_returns_empty_when_member_catalog_is_empty() {
    let response: ComponentsResponse = serde_json::from_str(r#"{"components":[]}"#)
        .expect("Given an empty member-scoped component catalog");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert!(resolved.specs.is_empty());
    assert!(resolved.discovered.is_empty());
    assert!(resolved.failures.is_empty());
}

#[test]
fn resolve_component_specs_selects_auto_core_sidecar_for_current_platform() {
    let response: ComponentsResponse = serde_json::from_str(r#"{
        "components":[{
            "kind":"core",
            "name":"samplecore",
            "version":"1.2.3",
            "runtime":"sidecar",
            "requiresDesktop":true,
            "rollout":"auto",
            "installed":true,
            "managed":true,
            "artifacts":{"darwin-arm64":{"url":"http://gw/v1/artifacts/sha256/abc","sha256":"abc"}},
            "sidecar":{"localUrl":"http://127.0.0.1:7190","listenEnv":"BNBLOC_SAMPLECORE_LISTEN","supervisedEnv":"BNBLOC_SAMPLECORE_SUPERVISED","args":["serve"]}
        }]
    }"#).expect("Given an auto sidecar core in the member catalog");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert_eq!(
        resolved.discovered,
        HashSet::from(["samplecore".to_string()])
    );
    assert!(resolved.failures.is_empty());
    assert_eq!(resolved.specs.len(), 1);
    let spec = &resolved.specs[0];
    assert_eq!(spec.name, "samplecore");
    assert_eq!(spec.kind, ComponentKind::Core);
    assert!(spec.binary);
    assert_eq!(spec.url, "http://gw/v1/artifacts/sha256/abc");
    assert_eq!(spec.sha256, "abc");
    assert_eq!(spec.local_url, "http://127.0.0.1:7190");
    assert_eq!(spec.listen_env, "BNBLOC_SAMPLECORE_LISTEN");
    assert_eq!(spec.supervised_env, "BNBLOC_SAMPLECORE_SUPERVISED");
    assert_eq!(spec.args, vec!["serve".to_string()]);
}

#[test]
fn resolve_component_specs_ignores_uninstalled_opt_in_components() {
    let response: ComponentsResponse = serde_json::from_str(
        r#"{
        "components":[{
            "kind":"plugin",
            "name":"room-auditor",
            "version":"0.1.0",
            "runtime":"sidecar",
            "requiresDesktop":true,
            "rollout":"optin",
            "installed":false,
            "managed":false
        }]
    }"#,
    )
    .expect("Given an opt-in component before member install");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert!(resolved.specs.is_empty());
    assert!(resolved.discovered.is_empty());
    assert!(resolved.failures.is_empty());
}

#[test]
fn resolve_component_specs_selects_installed_opt_in_plugin_sidecar() {
    let response: ComponentsResponse = serde_json::from_str(
        r#"{
        "components":[{
            "kind":"plugin",
            "name":"room-auditor",
            "version":"0.1.0",
            "runtime":"sidecar",
            "requiresDesktop":true,
            "rollout":"optin",
            "installed":true,
            "managed":false,
            "hooks":["chat_message.v1"],
            "artifacts":{"darwin-arm64":{"url":"http://gw/v1/artifacts/sha256/def","sha256":"def"}},
            "sidecar":{"localUrl":"http://127.0.0.1:7191"}
        }]
    }"#,
    )
    .expect("Given an installed opt-in plugin sidecar");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert_eq!(
        resolved.discovered,
        HashSet::from(["plugin/room-auditor".to_string()])
    );
    assert!(resolved.failures.is_empty());
    assert_eq!(resolved.specs[0].kind, ComponentKind::Plugin);
    assert_eq!(resolved.specs[0].local_url, "http://127.0.0.1:7191");
}

#[test]
fn resolve_component_specs_does_not_activate_off_components_even_if_sent() {
    let response: ComponentsResponse = serde_json::from_str(
        r#"{
        "components":[{
            "kind":"core",
            "name":"disabled-core",
            "version":"1.0.0",
            "runtime":"sidecar",
            "requiresDesktop":true,
            "rollout":"off",
            "installed":true,
            "managed":false,
            "artifacts":{"darwin-arm64":{"url":"http://gw/v1/artifacts/sha256/aaa","sha256":"aaa"}},
            "sidecar":{"localUrl":"http://127.0.0.1:7192"}
        }]
    }"#,
    )
    .expect("Given a defensive off component entry");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert!(resolved.specs.is_empty());
    assert!(resolved.discovered.is_empty());
    assert!(resolved.failures.is_empty());
}

#[test]
fn resolve_component_specs_records_artifact_failure_without_spec() {
    let response: ComponentsResponse = serde_json::from_str(
        r#"{
        "components":[{
            "kind":"core",
            "name":"linux-only",
            "version":"1.0.0",
            "runtime":"sidecar",
            "requiresDesktop":true,
            "rollout":"auto",
            "installed":true,
            "managed":true,
            "artifacts":{"linux-amd64":{"url":"http://gw/v1/artifacts/sha256/aaa","sha256":"aaa"}},
            "sidecar":{"localUrl":"http://127.0.0.1:7192"}
        }]
    }"#,
    )
    .expect("Given an installed component without this platform artifact");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert!(resolved.specs.is_empty());
    assert_eq!(
        resolved.discovered,
        HashSet::from(["linux-only".to_string()])
    );
    assert_eq!(resolved.failures.len(), 1);
    assert_eq!(resolved.failures[0].name, "linux-only");
    assert!(resolved.failures[0].message.contains("darwin-arm64"));
}

#[test]
fn resolve_component_specs_selects_manifest_core_data_without_binary() {
    let response: ComponentsResponse = serde_json::from_str(
        r#"{
        "components":[{
            "kind":"core",
            "name":"acme",
            "version":"0.5.0",
            "runtime":"manifest",
            "rollout":"auto",
            "installed":true,
            "managed":true,
            "manifest":{"name":"acme","createWorkspace":{"method":"POST","path":"/v1/workspaces"}}
        }]
    }"#,
    )
    .expect("Given an installed manifest core");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert_eq!(resolved.specs.len(), 1);
    assert_eq!(resolved.specs[0].kind, ComponentKind::Core);
    assert!(!resolved.specs[0].binary);
    assert!(resolved.specs[0].manifest.is_some());
}

#[test]
fn plugin_spec_carries_host_permissions_and_local_settings() {
    let response: ComponentsResponse = serde_json::from_str(
        r#"{
        "components":[{
            "kind":"plugin","name":"personal-bot","version":"0.1.0","runtime":"sidecar",
            "requiresDesktop":true,"rollout":"optin","installed":true,"managed":true,
            "hooks":["notification.v1"],
            "permissions":{"network":["127.0.0.1"],"host":["chat"]},
            "localSettings":[{"key":"appKey","label":"App key","secret":true,"required":true},{"key":"note","label":"Note"}],
            "artifacts":{"darwin-arm64":{"url":"http://gw/v1/artifacts/sha256/abc","sha256":"abc"}},
            "sidecar":{"localUrl":"http://127.0.0.1:7193","supervisedEnv":"PB_SUPERVISED"}
        },{
            "kind":"core","name":"samplecore","version":"1.0.0","runtime":"sidecar",
            "requiresDesktop":true,"rollout":"auto","installed":true,"managed":true,
            "permissions":{"host":["chat"]},
            "artifacts":{"darwin-arm64":{"url":"http://gw/x","sha256":"def"}},
            "sidecar":{"localUrl":"http://127.0.0.1:7190"}
        }]
    }"#,
    )
    .expect("catalog");
    let resolved = resolve_component_specs(&response, "darwin-arm64");
    let plugin = resolved
        .specs
        .iter()
        .find(|s| s.kind == ComponentKind::Plugin)
        .expect("plugin spec");
    assert_eq!(plugin.plugin.host, vec!["chat".to_string()]);
    assert_eq!(plugin.plugin.local_settings.len(), 2);
    assert!(plugin.plugin.local_settings[0].secret && plugin.plugin.local_settings[0].required);
    assert!(!plugin.plugin.local_settings[1].required);
    let core = resolved
        .specs
        .iter()
        .find(|s| s.kind == ComponentKind::Core)
        .expect("core spec");
    assert_eq!(
        core.plugin,
        PluginLaunch::default(),
        "cores never get plugin launch inputs"
    );
}

fn sandbox_entry(name: &str) -> String {
    format!(
        r#"{{
            "kind":"sandbox",
            "name":"{name}",
            "version":"0.1.0",
            "runtime":"sidecar",
            "contract":"bnbloc.sandbox.contract/v1",
            "requiresDesktop":true,
            "rollout":"auto",
            "installed":true,
            "managed":true,
            "capabilities":["exec","spaces","shares"],
            "protocol":{{"min":"1.0","max":"1.0"}},
            "guest":{{"image":"registry.example/toolbox:1","cpus":2,"memoryMiB":2048}},
            "artifacts":{{
                "darwin-arm64":{{"url":"http://gw/v1/artifacts/sha256/aaa","sha256":"aaa"}},
                "darwin-arm64/vmm":{{"url":"http://gw/v1/artifacts/sha256/bbb","sha256":"bbb"}},
                "darwin-arm64/bnbloc-vmexec":{{"url":"http://gw/v1/artifacts/sha256/ccc","sha256":"ccc"}}
            }},
            "sidecar":{{"localUrl":"http://127.0.0.1:7311","listenEnv":"SBX_LISTEN","supervisedEnv":"SBX_SUPERVISED",
                "tokenEnv":"SBX_TOKEN","dataDirEnv":"SBX_DATA_DIR",
                "files":[{{"name":"vmm","env":"SBX_VMM_PATH","executable":true}},
                         {{"name":"bnbloc-vmexec","env":"SBX_VMEXEC_PATH","executable":true}}]}}
        }}"#
    )
}

#[test]
fn resolve_component_specs_supervises_one_sandbox_runtime_with_its_files() {
    let raw = format!(
        r#"{{"components":[{},{},{{
            "kind":"core","name":"samplecore","version":"1.2.3","runtime":"sidecar",
            "requiresDesktop":true,"rollout":"auto","installed":true,"managed":true,
            "artifacts":{{"darwin-arm64":{{"url":"http://gw/v1/artifacts/sha256/abc","sha256":"abc"}}}},
            "sidecar":{{"localUrl":"http://127.0.0.1:7190"}}
        }}]}}"#,
        sandbox_entry("zeta-vm"),
        sandbox_entry("alpha-vm")
    );
    let response: ComponentsResponse =
        serde_json::from_str(&raw).expect("Given two sandbox runtimes beside a core");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert_eq!(
        resolved.discovered,
        HashSet::from([
            "samplecore".to_string(),
            "sandbox/alpha-vm".to_string(),
            "sandbox/zeta-vm".to_string()
        ]),
        "the inactive sandbox stays discovered, so it is not treated as uninstalled"
    );
    assert!(resolved.failures.is_empty());
    assert_eq!(resolved.inactive.len(), 1);
    assert_eq!(resolved.inactive[0].name, "zeta-vm");
    assert!(resolved.inactive[0].message.contains("alpha-vm is active"));
    assert_eq!(resolved.specs.len(), 2);
    let sandbox = resolved
        .specs
        .iter()
        .find(|s| s.kind == ComponentKind::Sandbox)
        .expect("the lexically first sandbox runs");
    assert_eq!(sandbox.name, "alpha-vm");
    assert_eq!(sandbox.id(), "sandbox/alpha-vm");
    assert_eq!(sandbox.sha256, "aaa");
    assert_eq!(sandbox.sandbox.token_env, "SBX_TOKEN");
    assert_eq!(sandbox.sandbox.data_dir_env, "SBX_DATA_DIR");
    assert_eq!(
        sandbox.sandbox.files,
        vec![
            SidecarFileSpec {
                name: "vmm".into(),
                env: "SBX_VMM_PATH".into(),
                executable: true,
                url: "http://gw/v1/artifacts/sha256/bbb".into(),
                sha256: "bbb".into(),
            },
            SidecarFileSpec {
                name: "bnbloc-vmexec".into(),
                env: "SBX_VMEXEC_PATH".into(),
                executable: true,
                url: "http://gw/v1/artifacts/sha256/ccc".into(),
                sha256: "ccc".into(),
            },
        ]
    );
    assert_eq!(
        sandbox.sandbox.guest,
        Some(GuestProfile {
            image: "registry.example/toolbox:1".into(),
            cpus: 2,
            memory_mib: 2048
        })
    );
    let core = resolved
        .specs
        .iter()
        .find(|s| s.kind == ComponentKind::Core)
        .expect("core");
    assert_eq!(
        core.sandbox,
        SandboxLaunch::default(),
        "cores get no sandbox launch inputs"
    );
}

#[test]
fn resolve_component_specs_refuses_a_sandbox_missing_a_platform_file() {
    let raw = format!(r#"{{"components":[{}]}}"#, sandbox_entry("vm")).replace(
        r#""darwin-arm64/vmm":{"url":"http://gw/v1/artifacts/sha256/bbb","sha256":"bbb"},"#,
        "",
    );
    let response: ComponentsResponse = serde_json::from_str(&raw).expect("catalog");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert!(resolved.specs.is_empty());
    assert_eq!(resolved.failures.len(), 1);
    assert!(
        resolved.failures[0].message.contains("darwin-arm64/vmm"),
        "{}",
        resolved.failures[0].message
    );
}

const PREVIEWER_ENTRY: &str = r#"{
    "kind":"previewer",
    "name":"sprite-sheet",
    "version":"1.0.0",
    "runtime":"bundle",
    "contract":"bnbloc.previewer.contract/v1",
    "rollout":"auto",
    "installed":true,
    "managed":true,
    "artifacts":{"bundle":{"url":"http://gw/v1/artifacts/sha256/abc","sha256":"abc"}},
    "previewer":{
        "entry":"index.html",
        "match":{"mimeTypes":["image/*"],"extensions":[".atlas.json"]},
        "rank":50,
        "capabilities":["thumbnail","toolbar"],
        "toolbar":[{"id":"grid","icon":"grid","label":"Grid","kind":"toggle"}],
        "bundleUrl":"http://gw/v1/artifacts/sha256/abc",
        "bundleSha256":"abc",
        "frameUrl":"/v1/previewers/sprite-sheet/1.0.0/t/",
        "frameExpiresAtMs":1
    }
}"#;

#[test]
fn a_previewer_in_the_catalog_is_never_supervised_and_breaks_nothing() {
    let raw = format!(
        r#"{{"components":[{PREVIEWER_ENTRY},{{
            "kind":"core","name":"samplecore","version":"1.2.3","runtime":"sidecar",
            "requiresDesktop":true,"rollout":"auto","installed":true,
            "artifacts":{{"darwin-arm64":{{"url":"http://gw/v1/artifacts/sha256/def","sha256":"def"}}}},
            "sidecar":{{"localUrl":"http://127.0.0.1:7190"}}
        }}]}}"#
    );
    let response: ComponentsResponse = serde_json::from_str(&raw)
        .expect("Given a catalog with a previewer bundle and a sidecar core");

    let resolved = resolve_component_specs(&response, "darwin-arm64");

    assert_eq!(resolved.specs.len(), 1);
    assert_eq!(resolved.specs[0].name, "samplecore");
    assert_eq!(
        resolved.discovered,
        HashSet::from(["samplecore".to_string()])
    );
    assert!(resolved.failures.is_empty() && resolved.inactive.is_empty());
    let previewer = response.components[0]
        .previewer
        .as_ref()
        .expect("previewer block");
    assert_eq!(previewer.bundle_sha256, "abc");
}

#[test]
fn a_malformed_previewer_block_does_not_fail_the_catalog() {
    let raw = r#"{"components":[{"kind":"previewer","name":"p","version":"1.0.0","runtime":"bundle",
        "rollout":"auto","installed":true,"previewer":{"entry":"index.html"}}]}"#;
    let response: ComponentsResponse = serde_json::from_str(raw).expect("decodes");
    assert!(resolve_component_specs(&response, "darwin-arm64")
        .specs
        .is_empty());
}
