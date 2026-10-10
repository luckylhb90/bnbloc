# bnbloc Control-Plane — operations.
#
# One command to run the whole product locally (control-plane + MySQL + web UI):
#   make up        # build images, start MySQL + the services, serve UI on :7090
#   make down      # stop everything (keeps the MySQL volume / data)
#   make clean     # stop and delete the MySQL volume (wipes data)
#   make logs      # follow all service logs
#   make ps        # show container status
#   make test      # run the Go test suite
#
# After `make up`, open http://localhost:7090 in a browser.
# To use a REAL local agent, install a core (README "Connect an agent") and run
# its daemon on the host. Site-specific compose overlays go in COMPOSE, e.g.
#   make up COMPOSE="docker compose -f compose.yml -f my-overlay.yml"

COMPOSE ?= docker compose -f compose.yml

.PHONY: up down clean logs ps build test web

up:
	$(COMPOSE) up -d --build
	@echo ""
	@echo "bnbloc is starting. Web client: http://localhost:7090"
	@echo "Run 'make logs' to follow, 'make ps' for status."

down:
	$(COMPOSE) down

clean:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f

ps:
	$(COMPOSE) ps

build:
	$(COMPOSE) build

# Everything CI runs (go vet/test, web, desktop, secrets): scripts/ci.sh
test:
	scripts/ci.sh go

# Build the web client into services/gateway/webui/static for local `go run`
# and `pnpm dev` work. The gateway IMAGE builds the SPA itself (Dockerfile
# web-full stage), so these assets are no longer committed.
web:
	cd web && pnpm install && pnpm build
