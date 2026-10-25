# bnbloc all-in-one production image.
#
# Builds all six services into one image and runs them as one container under
# a small supervisor, exposing only the gateway on :8080. This exists for CI
# platforms that build exactly one Dockerfile with no build args and run one
# container per pipeline (the form the enterprise CI edit screen enforces).
#
# For a scaled deployment prefer the per-service images in
# deploy/prod/<service>/Dockerfile, one CI pipeline each.
#
#   docker build -f deploy/prod/Dockerfile -t bnbloc-aio .
#   docker run -p 8080:8080 --env-file .env.production bnbloc-aio
#
# Every service is a Go process addressed over loopback inside the container.
# Room defaults to :8080, which the gateway also uses, so room is moved to
# :8086 here; all other ports keep their defaults (8081-8083, 8085).

ARG GO_VERSION=1.26
ARG NODE_VERSION=22
ARG ALPINE_VERSION=3.22

# --- SPA (only the gateway embeds it) -----------------------------------
FROM node:${NODE_VERSION}-alpine${ALPINE_VERSION} AS web
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm config set ignore-scripts true && pnpm install --frozen-lockfile
COPY web/ ./
# Build identity, as in the root Dockerfile: version from the desktop package,
# commit from the build arg (.git is not in the build context).
COPY apps/desktop/package.json /apps/desktop/package.json
ARG CI_COMMIT_SHA=""
ENV CI_COMMIT_SHA=${CI_COMMIT_SHA}
RUN sed -i 's#\.\./services/gateway/webui/static#/out/services/gateway/webui/static#' vite.config.ts \
    && pnpm build

# --- Build the service binaries (+ the operator CLI) --------------------
# The CLI (/usr/local/bin/bnbloc) is not supervised; it is there so an
# operator or a host scheduler can run e.g. the nightly smoke inside the
# container: `docker exec <aio> bnbloc smoke --gateway http://127.0.0.1:8080`
# (docs/production-deployment.md#nightly-smoke).
FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS build
ARG GO_TAGS=""
WORKDIR /src
COPY go.mod ./
COPY . .
COPY --from=web /out/services/gateway/webui/static ./services/gateway/webui/static
# Build identity (internal/buildinfo, served at /version).
ARG CI_COMMIT_SHA=""
RUN set -eux; \
    bi=github.com/luckylhb90/bnbloc/internal/buildinfo; \
    ldflags="-X $bi.Commit=${CI_COMMIT_SHA} -X $bi.BuiltAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
    for svc in gateway room realtime activity shareagent relay pulse; do \
      CGO_ENABLED=0 go build -trimpath -tags "${GO_TAGS}" -ldflags "$ldflags" \
        -o /out/bnbloc-${svc} ./services/${svc}/cmd; \
    done; \
    CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o /out/bnbloc ./cmd/bnbloc

# --- Runtime -------------------------------------------------------------
# One container runs the services as processes, which needs a shell and a process
# supervisor — neither exists in a distroless image. Alpine keeps the image
# small while providing both. tini is PID 1 for correct signal and zombie
# handling; the entrypoint supervises the services and exits the container if
# any one dies, so the orchestrator restarts the whole unit rather than
# silently serving a half-dead stack.
FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache tini ca-certificates wget \
    && addgroup -S bnbloc && adduser -S -G bnbloc bnbloc \
    && mkdir -p /var/lib/bnbloc/attachments && chown -R bnbloc:bnbloc /var/lib/bnbloc
COPY --from=build /out/ /usr/local/bin/
COPY deploy/prod/aio-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
# Attachments live here; mount a volume at /var/lib/bnbloc/attachments to persist.
ENV BNBLOC_ATTACHMENTS_DIR=/var/lib/bnbloc/attachments
EXPOSE 8080
# Readiness of the fronting gateway (DB-gated). Per-service liveness is the
# supervisor's job: any exited service takes the container down.
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/readyz || exit 1
USER bnbloc:bnbloc
ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
