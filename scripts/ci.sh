#!/usr/bin/env bash
# Runner-agnostic CI entrypoint: the single list of checks every pipeline runs.
# .github/workflows/ci.yml calls one stage per job; any other runner (or a
# developer before pushing) calls the same stages, so the check list lives in
# exactly one place.
#
#   scripts/ci.sh go        generated-file drift checks (Dockerfiles, schema copy,
#                           wire.gen.ts), go vet, go test
#                           (MySQL integration tests run when
#                           BNBLOC_TEST_MYSQL_DSN is set, skip otherwise;
#                           they need CREATE/DROP DATABASE on its server)
#   scripts/ci.sh web       pnpm install --frozen-lockfile, build, node tests, knip
#   scripts/ci.sh desktop   web build + bridge sidecar + cargo test --locked
#   scripts/ci.sh secrets   gitleaks over the git history (needs gitleaks)
#   scripts/ci.sh all       every stage above, in order
#
# Toolchain: Go (go.mod), Node 24 (node --test strips TypeScript natively),
# pnpm 10, Rust stable, gitleaks 8. The desktop stage needs a host the Tauri
# app builds on (macOS, or Linux with the webkit2gtk-4.1 development packages).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

log() { printf '\n==> %s\n' "$*"; }

# //go:embed static/* in services/gateway/webui needs at least one file. The
# real SPA is built by the web stage (or the gateway image); Go-only checks get
# the same placeholder the service images use.
ensure_webui_placeholder() {
  local static="services/gateway/webui/static"
  if [ -z "$(ls -A "$static" 2>/dev/null)" ]; then
    mkdir -p "$static"
    printf '<!doctype html><title>bnbloc</title>' > "$static/index.html"
  fi
}

stage_go() {
  log "deploy/prod Dockerfiles in sync with /Dockerfile"
  deploy/prod/gen-dockerfiles.sh --check
  log "deploy/schema.sql is a verbatim copy of internal/store/schema.sql"
  cmp deploy/schema.sql internal/store/schema.sql
  log "web/src/lib/wire.gen.ts generated from internal/wire (go generate ./internal/wire)"
  go run ./internal/wire/cmd/wiregen -check
  ensure_webui_placeholder
  log "go vet ./..."
  go vet ./...
  log "go test ./...${BNBLOC_TEST_MYSQL_DSN:+ (with MySQL integration tests)}"
  go test -count=1 ./...
}

web_install_build() {
  log "web: pnpm install --frozen-lockfile"
  pnpm --dir web install --frozen-lockfile
  log "web: pnpm build"
  pnpm --dir web build
}

# The gateway image builds the SPA from web/ alone (plus the desktop
# package.json for the version stamp), so a production import of any other
# repo file breaks the deploy while in-repo builds pass. Rebuild the same
# layout in a temp dir.
web_isolated_build() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN
  mkdir -p "$tmp/apps/desktop"
  cp apps/desktop/package.json "$tmp/apps/desktop/package.json"
  (cd web && tar --exclude=node_modules --exclude='*.tsbuildinfo' -cf - .) | (mkdir -p "$tmp/web" && cd "$tmp/web" && tar -xf -)
  (cd "$tmp/web" && pnpm install --frozen-lockfile --silent && pnpm build >/dev/null)
}

stage_web() {
  web_install_build
  log "web: type-check tests (tsconfig.test.json; the build excludes them)"
  pnpm --dir web run typecheck:tests
  log "web: production build from web/ alone (what the image's web stage sees)"
  web_isolated_build
  log "web: node --test"
  (cd web && node --test 'src/**/*.test.ts' 'scripts/*.test.mjs' 'previewer-sdk/src/*.test.ts')
  log "web: knip (no unused files, exports or dependencies)"
  pnpm --dir web exec knip
  log "web: reference previewer build"
  node --test examples/previewers/sprite-sheet/build.test.mjs
}

stage_desktop() {
  web_install_build
  log "desktop: sidecar script tests + build bridge sidecar"
  node --test apps/desktop/scripts/*.test.mjs
  node apps/desktop/scripts/build-bridge-sidecar.mjs
  log "desktop: cargo test --locked"
  cargo test --locked --manifest-path apps/desktop/src-tauri/Cargo.toml
}

# A made-up token in a test carries an inline `gitleaks:allow` comment.
stage_secrets() {
  log "secrets: gitleaks over the git history"
  gitleaks git --redact --no-banner .
}

case "${1:-all}" in
  go) stage_go ;;
  web) stage_web ;;
  desktop) stage_desktop ;;
  secrets) stage_secrets ;;
  all)
    stage_go
    stage_web
    stage_desktop
    stage_secrets
    ;;
  *)
    echo "usage: scripts/ci.sh [go|web|desktop|secrets|all]" >&2
    exit 2
    ;;
esac
