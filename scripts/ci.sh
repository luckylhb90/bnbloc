#!/usr/bin/env bash
# Runner-agnostic CI entrypoint: the single list of checks every pipeline runs.
# .github/workflows/ci.yml calls one stage per job; any other runner (or a
# developer before pushing) calls the same stages, so the check list lives in
# exactly one place.
#
#   scripts/ci.sh go        generated-file drift checks, go vet, go test
#                           (MySQL integration tests run when
#                           BNBLOC_TEST_MYSQL_DSN is set, skip otherwise)
#   scripts/ci.sh web       pnpm install --frozen-lockfile, build, node tests
#   scripts/ci.sh desktop   web build + bridge sidecar + cargo test --locked
#   scripts/ci.sh boundary  public-boundary scan of the tracked tree, including
#                           the manifest's public builds (needs go, node, pnpm,
#                           cargo). Skipped when the private manifest is absent
#                           (a public checkout has nothing private to scan).
#   scripts/ci.sh all       every stage above, in order
#
# Toolchain: Go (go.mod), Node 24 (node --test strips TypeScript natively),
# pnpm 10, Rust stable. The desktop and boundary stages need a host the Tauri
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

stage_web() {
  web_install_build
  log "web: node --test"
  (cd web && node --test 'src/**/*.test.ts' 'scripts/*.test.mjs' 'previewer-sdk/src/*.test.ts')
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

stage_boundary() {
  if [ ! -f .components/private-manifest.json ]; then
    log "boundary: no private manifest in this checkout; nothing to scan"
    return 0
  fi
  log "boundary: scan tracked files + public builds"
  go run ./scripts/check-public-boundary
}

case "${1:-all}" in
  go) stage_go ;;
  web) stage_web ;;
  desktop) stage_desktop ;;
  boundary) stage_boundary ;;
  all)
    stage_go
    stage_web
    stage_desktop
    stage_boundary
    ;;
  *)
    echo "usage: scripts/ci.sh [go|web|desktop|boundary|all]" >&2
    exit 2
    ;;
esac
