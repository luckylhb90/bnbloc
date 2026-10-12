//! Per-launch secrets and the one comparison of them.
//!
//! Every random value the shell hands out (a plugin's host API token, a
//! sandbox launch's bearer, OAuth PKCE verifiers and `state`, temp-file tags)
//! comes from `random_bytes` (the OS CSPRNG); every check of a presented
//! secret is `ct_eq` (constant time). A `Secret` never prints: Debug and
//! Display redact it, so a status dump or an error log can never carry one.

/// `n` bytes from the OS CSPRNG.
pub(crate) fn random_bytes(n: usize) -> Result<Vec<u8>, String> {
    let mut raw = vec![0u8; n];
    getrandom::fill(&mut raw).map_err(|e| format!("random: {e}"))?;
    Ok(raw)
}

/// Whether two secrets are equal, in time independent of where they differ.
pub(crate) fn ct_eq(a: &str, b: &str) -> bool {
    let (a, b) = (a.as_bytes(), b.as_bytes());
    if a.len() != b.len() {
        return false;
    }
    a.iter().zip(b).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

/// A per-launch bearer: 256 random bits, hex.
#[derive(Clone, PartialEq, Eq)]
pub(crate) struct Secret(String);

impl Secret {
    pub(crate) fn generate() -> Result<Self, String> {
        let raw = random_bytes(32)?;
        Ok(Self(raw.iter().map(|b| format!("{b:02x}")).collect()))
    }

    #[cfg(test)]
    pub(crate) fn from_test(value: &str) -> Self {
        Self(value.to_string())
    }

    pub(crate) fn expose(&self) -> &str {
        &self.0
    }

    /// Whether `presented` is this secret (constant time; an empty one
    /// never is).
    pub(crate) fn matches(&self, presented: &str) -> bool {
        !presented.is_empty() && ct_eq(&self.0, presented)
    }
}

impl std::fmt::Debug for Secret {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Secret(<redacted>)")
    }
}

impl std::fmt::Display for Secret {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("<redacted>")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn secrets_are_fresh_redacted_and_compared_whole() {
        let a = Secret::generate().unwrap();
        let b = Secret::generate().unwrap();
        assert_ne!(a, b);
        assert_eq!(a.expose().len(), 64);
        assert!(!format!("{a:?} {a}").contains(a.expose()));
        assert!(a.matches(a.expose()));
        assert!(!a.matches(b.expose()));
        assert!(!a.matches(""));
        assert!(ct_eq("abc", "abc"));
        assert!(!ct_eq("abc", "abd"));
        assert!(!ct_eq("abc", "abcd"));
        assert!(!ct_eq("", "a"));
    }
}
