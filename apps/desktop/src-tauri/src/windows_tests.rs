use super::*;

fn key(subject: &str) -> AccountKey {
    AccountKey::new("https://gw.example.com", subject).unwrap()
}

/// The glob a capability's `windows` entries use: a literal, or a prefix and
/// `*`.
fn matches_a_capability(label: &str) -> bool {
    let capability: serde_json::Value =
        serde_json::from_str(include_str!("../capabilities/default.json")).unwrap();
    capability["windows"]
        .as_array()
        .unwrap()
        .iter()
        .filter_map(|w| w.as_str())
        .any(|pattern| match pattern.strip_suffix('*') {
            Some(prefix) => label.starts_with(prefix),
            None => pattern == label,
        })
}

/// Every label the app builds matches a capability: a window that matches
/// none gets no IPC, not even the events its page listens for.
#[test]
fn every_window_label_matches_a_capability() {
    let maya = key("maya");
    let mut labels = vec![MAIN.to_string(), app_label(Some(&maya)), app_label(None)];
    for kind in [AuxKind::Preview, AuxKind::People, AuxKind::Live] {
        labels.push(aux_label(kind, &maya, "room 1/artifact:2"));
    }
    for label in &labels {
        assert!(matches_a_capability(label), "{label} matches no capability");
        assert!(
            label
                .chars()
                .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_'),
            "{label} leaves Tauri's label alphabet"
        );
    }
}

/// The label names the account without ever carrying its subject or origin,
/// and two accounts' windows for one artifact are two windows.
#[test]
fn a_label_names_the_account_by_its_short_name() {
    let (maya, jack) = (key("maya"), key("jack"));
    let label = aux_label(AuxKind::Preview, &maya, "artf_1");
    assert_eq!(label, format!("preview-{}-artf_1", maya.short()));
    assert!(!label.contains("maya") && !label.contains("example"));
    assert_ne!(label, aux_label(AuxKind::Preview, &jack, "artf_1"));
    assert_ne!(
        app_label(Some(&maya)),
        app_label(Some(&maya)),
        "each window its own"
    );
    assert_eq!(maya.short().len(), 8);
    assert_eq!(label_part("a/b:c d"), "a_b_c_d");
}

/// The app ends with its last app window; an auxiliary window never counts
/// and never ends it.
#[test]
fn the_app_ends_with_its_last_app_window() {
    let (maya, jack) = (key("maya"), key("jack"));
    let (a, b) = (app_label(Some(&maya)), app_label(Some(&jack)));
    let preview = aux_label(AuxKind::Preview, &maya, "x");
    let all = [MAIN, a.as_str(), b.as_str(), preview.as_str()];
    assert!(is_app_window(MAIN) && is_app_window(&a) && !is_app_window(&preview));
    assert_eq!(app_windows_left(all, &a), 2, "two others remain");
    assert_eq!(
        app_windows_left(all, &preview),
        3,
        "a preview is not an app window"
    );
    assert_eq!(
        app_windows_left([a.as_str(), preview.as_str()], &a),
        0,
        "only a preview left: the app ends"
    );
    assert_eq!(app_windows_left([MAIN], MAIN), 0);
}

/// The hash a new window opens on is exactly what the web reads
/// (windowBinding.ts): both spell it the same.
#[test]
fn the_intent_hash_is_the_one_the_web_reads() {
    assert_eq!(intent_hash(None), "#!add-account");
    assert_eq!(
        intent_hash(Some("https://gw.example.com\nsubject@x")),
        "#!account=https%3A%2F%2Fgw.example.com%0Asubject%40x"
    );
    let web = include_str!("../../../../web/src/lib/windowBinding.ts");
    assert!(
        web.contains("const ADD = \"#!add-account\""),
        "the web's add intent"
    );
    assert!(
        web.contains("const ACCOUNT = \"#!account=\""),
        "the web's account intent"
    );
}
