# Secret scan in CI

Status: landed 2026-10-10.

## Intent

The repository is developed in the open. A credential committed by mistake
must fail CI even if a later commit removes it, because the history stays
public.

## Design

- `scripts/ci.sh secrets` runs `gitleaks git --redact` over the git history.
  The `secrets` job in `.github/workflows/ci.yml` checks out the full history
  (`fetch-depth: 0`), installs a pinned gitleaks release and runs that stage.
- A deliberately fake token in a test carries an inline `gitleaks:allow`
  comment rather than a repo-wide allowlist, so each exception is visible
  where it lives. `.gitleaksignore` holds only the fingerprint of that same
  token in the initial commit, which predates the comment.
- The former `boundary` stage is gone: this repository no longer contains
  private material to filter out.

## If the scan fails

Treat the credential as leaked: revoke and rotate it first, then remove it
from the code. Rewriting history does not un-publish a pushed secret.
