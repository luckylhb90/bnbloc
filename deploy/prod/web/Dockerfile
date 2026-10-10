# bnbloc web — static SPA image.
#
# Builds the web client and serves it as static files (nginx). It does NOT
# reverse-proxy the API or relay: the front nginx / ALB owns path routing
# (/v1 /api /sso -> gateway, /relay -> relay). This image has one job — host the
# SPA with history fallback — so routing lives in exactly one place upstream.
#
# The SPA calls the backend over same-origin paths, so the front proxy must put
# this container and the backend services under one host.
#
#   docker build -f deploy/prod/web/Dockerfile -t bnbloc-web .
#   docker run -p 8080:8080 bnbloc-web

ARG NODE_VERSION=22
ARG ALPINE_VERSION=3.22

FROM node:${NODE_VERSION}-alpine${ALPINE_VERSION} AS build
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm config set ignore-scripts true && pnpm install --frozen-lockfile
COPY web/ ./
# vite.config.ts writes to ../services/gateway/webui/static; retarget it to a
# local dist so this build needs no Go tree in context.
RUN sed -i 's#\.\./services/gateway/webui/static#dist#' vite.config.ts \
    && pnpm build

# Non-root: the unprivileged nginx build runs as uid 101 with its pid/temp
# paths moved to writable locations, and listens on 8080 (no capability to
# bind :80 needed). The stock nginx image would run the master as root.
FROM nginxinc/nginx-unprivileged:1.28-alpine3.21
COPY --from=build /web/dist /usr/share/nginx/html
# The config has no runtime variables (no reverse proxy), so it goes straight
# into conf.d — no envsubst templating needed.
COPY deploy/prod/web/nginx.conf /etc/nginx/conf.d/default.conf
USER 101
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
