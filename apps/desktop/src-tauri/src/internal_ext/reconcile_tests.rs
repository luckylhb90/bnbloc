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

    let applied = apply_specs_to_inner(&mut inner, vec![spec.clone()], discovered, 0, false);

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
            pending: None,
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

    let applied = apply_specs_to_inner(&mut inner, Vec::new(), HashSet::new(), 0, false);

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
            pending: None,
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
        0,
        false,
    );

    assert_eq!(applied.generation_to_start, Some(7));
    let sidecar = inner.sidecars.get("samplecore").expect("status retained");
    assert!(sidecar.spec.is_none());
    assert!(sidecar.runtime_url.is_none());
    assert_eq!(sidecar.generation, 5);
}

fn running_core(spec: SidecarSpec) -> ManagedSidecar {
    let mut sidecar = ManagedSidecar::pending(AdapterStatus::ready(&spec));
    sidecar.spec = Some(spec);
    sidecar.runtime_url = Some("http://127.0.0.1:7190".to_string());
    sidecar.supervisor_active = true;
    sidecar.generation = 2;
    sidecar
}

/// A running core's update waits while turns run (live turns: a turn has
/// no fixed length) and applies at the first idle moment.
#[test]
fn a_running_cores_update_waits_for_an_idle_moment() {
    let mut inner = inner_with_desired_up();
    let old = core_spec("samplecore");
    inner.sidecars.insert(old.id(), running_core(old.clone()));
    let mut new = core_spec("samplecore");
    new.version = "1.1.0".to_string();

    let applied = apply_specs_to_inner(
        &mut inner,
        vec![new.clone()],
        HashSet::from([new.id()]),
        2,
        false,
    );
    assert!(
        applied.removed.is_empty(),
        "nothing stopped while turns run"
    );
    let sidecar = &inner.sidecars["samplecore"];
    assert_eq!(
        sidecar.spec.as_ref().map(|s| s.version.as_str()),
        Some("1.0.0"),
        "the old one keeps running"
    );
    assert_eq!(
        sidecar.pending.as_ref().map(|s| s.version.as_str()),
        Some("1.1.0")
    );
    assert_eq!(sidecar.status.state, "update-pending");
    assert!(
        sidecar
            .status
            .message
            .contains("waiting for 2 running tasks"),
        "{}",
        sidecar.status.message
    );

    // Still busy: it keeps waiting.
    let applied = apply_pending_in(&mut inner, 1, false);
    assert_eq!(applied.generation_to_start, None);
    assert!(inner.sidecars["samplecore"].pending.is_some());

    // Idle: the update applies.
    let applied = apply_pending_in(&mut inner, 0, false);
    assert_eq!(applied.generation_to_start, Some(7));
    let sidecar = &inner.sidecars["samplecore"];
    assert_eq!(
        sidecar.spec.as_ref().map(|s| s.version.as_str()),
        Some("1.1.0")
    );
    assert!(sidecar.pending.is_none());
    assert_eq!(sidecar.generation, 3);
}

/// "Apply now" goes ahead while turns run; a stopped sidecar never waits.
#[test]
fn apply_now_and_stopped_sidecars_do_not_wait() {
    let mut inner = inner_with_desired_up();
    let old = core_spec("samplecore");
    inner.sidecars.insert(old.id(), running_core(old.clone()));
    let mut new = core_spec("samplecore");
    new.version = "1.1.0".to_string();
    apply_specs_to_inner(
        &mut inner,
        vec![new.clone()],
        HashSet::from([new.id()]),
        3,
        true,
    );
    assert_eq!(
        inner.sidecars["samplecore"]
            .spec
            .as_ref()
            .map(|s| s.version.as_str()),
        Some("1.1.0")
    );

    let mut stopped = ManagedSidecar::pending(AdapterStatus::ready(&old));
    stopped.spec = Some(old.clone());
    inner.sidecars.insert(old.id(), stopped);
    apply_specs_to_inner(
        &mut inner,
        vec![new.clone()],
        HashSet::from([new.id()]),
        3,
        false,
    );
    assert_eq!(
        inner.sidecars["samplecore"]
            .spec
            .as_ref()
            .map(|s| s.version.as_str()),
        Some("1.1.0")
    );
}

/// The catalog going back to what runs drops the waiting update.
#[test]
fn a_pending_update_withdrawn_by_the_catalog_is_dropped() {
    let mut inner = inner_with_desired_up();
    let old = core_spec("samplecore");
    inner.sidecars.insert(old.id(), running_core(old.clone()));
    let mut new = core_spec("samplecore");
    new.version = "1.1.0".to_string();
    apply_specs_to_inner(
        &mut inner,
        vec![new.clone()],
        HashSet::from([new.id()]),
        1,
        false,
    );
    apply_specs_to_inner(
        &mut inner,
        vec![old.clone()],
        HashSet::from([old.id()]),
        1,
        false,
    );
    assert!(inner.sidecars["samplecore"].pending.is_none());
}
