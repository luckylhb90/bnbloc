use std::collections::{HashMap, HashSet};

use super::*;
use sidecar_model::ComponentKind;

fn inner_with_desired_up() -> Inner {
    Inner {
        desired_up: true,
        generation: 7,
        sidecars: HashMap::new(),
        bridge_cores: HashMap::new(),
    }
}

fn core_spec(name: &str) -> SidecarSpec {
    SidecarSpec {
        kind: ComponentKind::Core,
        name: name.to_string(),
        version: "1.0.0".to_string(),
        artifact: Some(sidecar_model::ComponentArtifact {
            url: "http://gateway/v1/artifacts/sha256/abc".to_string(),
            sha256: pin::Sha256Pin::of(b"abc"),
        }),
        manifest: None,
        local_url: "http://127.0.0.1:7190".to_string(),
        listen_env: "BNBLOC_CORE_LISTEN".to_string(),
        supervised_env: String::new(),
        args: Vec::new(),
        plugin: Default::default(),
        sandbox: Default::default(),
    }
}

#[test]
fn reconcile_specs_rehydrates_restart_state_without_marking_removed_components_active() {
    let mut inner = inner_with_desired_up();
    let spec = core_spec("samplecore");
    let discovered = HashSet::from([spec.id()]);

    let applied = apply_specs_to_inner(&mut inner, vec![spec.clone()], discovered);

    assert_eq!(applied.generation_to_start, Some(7));
    let sidecar = inner
        .sidecars
        .get("samplecore")
        .expect("samplecore rehydrated");
    let got = sidecar.spec.as_ref().expect("samplecore spec stored");
    assert_eq!(got.name, spec.name);
    assert_eq!(got.kind, spec.kind);
    assert_eq!(sidecar.status.state, "ready");
}

#[test]
fn reconcile_specs_removes_uninstalled_component_state() {
    let mut inner = inner_with_desired_up();
    let old = core_spec("samplecore");
    inner.sidecars.insert(
        old.id(),
        ManagedSidecar {
            spec: Some(old),
            runtime_url: Some("http://127.0.0.1:7190".to_string()),
            child: None,
            generation: 3,
            supervisor_active: false,
            refused: None,
            status: AdapterStatus::new(
                ComponentKind::Core,
                "samplecore",
                "ready",
                "ready".to_string(),
                None,
                Some("1.0.0".to_string()),
            ),
        },
    );

    let applied = apply_specs_to_inner(&mut inner, Vec::new(), HashSet::new());

    assert_eq!(applied.generation_to_start, Some(7));
    assert!(!inner.sidecars.contains_key("samplecore"));
}

#[test]
fn reconcile_specs_clears_process_state_when_active_component_loses_artifact() {
    let mut inner = inner_with_desired_up();
    let old = core_spec("samplecore");
    inner.sidecars.insert(
        old.id(),
        ManagedSidecar {
            spec: Some(old),
            runtime_url: Some("http://127.0.0.1:7190".to_string()),
            child: None,
            supervisor_active: true,
            generation: 4,
            refused: None,
            status: AdapterStatus::new(
                ComponentKind::Core,
                "samplecore",
                "waiting",
                "Waiting for samplecore artifact: missing".to_string(),
                None,
                Some("1.0.0".to_string()),
            ),
        },
    );

    let applied = apply_specs_to_inner(
        &mut inner,
        Vec::new(),
        HashSet::from(["samplecore".to_string()]),
    );

    assert_eq!(applied.generation_to_start, Some(7));
    let sidecar = inner.sidecars.get("samplecore").expect("status retained");
    assert!(sidecar.spec.is_none());
    assert!(sidecar.runtime_url.is_none());
    assert_eq!(sidecar.generation, 5);
}
