#!/bin/sh
# bnbloc all-in-one supervisor.
#
# Starts the services as background processes inside one container and
# ties their lifetimes together: if any one exits, the whole container exits so
# the orchestrator restarts the unit, rather than leaving the gateway serving
# requests that fail downstream. tini (PID 1) reaps zombies and forwards
# signals to this script.
#
# Every service resolves its peers over loopback. Room defaults to :8080, which
# the gateway also binds, so room is pinned to :8086 here; the rest keep their
# defaults. External inputs (database DSN, secrets, identity) come from the
# environment and are NOT defaulted — a missing secret must fail the service.
set -u

# This is a production image: every dev-permissive fallback (unsigned
# internal identity, dev login verifier, in-memory store, ephemeral session
# key) is a fatal startup error unless the operator explicitly opts out.
export BNBLOC_ENV="${BNBLOC_ENV:-production}"

# Room off the gateway's :8080.
export BNBLOC_ROOM_LISTEN_ADDR=":8086"

# Internal wiring: every service talks to its peers over loopback. These are
# fixed by the in-container topology, so they are set here rather than asked of
# the operator.
export BNBLOC_ROOM_BASE_URL="http://127.0.0.1:8086"
export BNBLOC_ACTIVITY_BASE_URL="http://127.0.0.1:8081"
export BNBLOC_REALTIME_BASE_URL="http://127.0.0.1:8082"
export BNBLOC_SHAREAGENT_BASE_URL="http://127.0.0.1:8083"
# Read by the gateway only to report the relay's /version (deployment view).
export BNBLOC_RELAY_BASE_URL="http://127.0.0.1:8085"

# The all-in-one is one node by construction: realtime and relay own every
# key and the gateway is the only replica. Scale-out peer sets (a Kubernetes
# deployment) must never leak in from the container environment — realtime
# would refuse its disk blob store and the gateway would divide its rate
# limits by replicas that do not exist.
unset BNBLOC_REALTIME_PEERS BNBLOC_REALTIME_SELF BNBLOC_RELAY_PEERS BNBLOC_RELAY_SELF BNBLOC_GATEWAY_PEERS

# Member notifications read BNBLOC_PUBLIC_URL (room deep links, shared by
# realtime, shareagent and activity's budget warnings; the gateway's links
# and artifact URLs) straight from the container environment; unset
# means no notification links and request-origin gateway links. Delivery targets are installed notifier components.
# The gateway reads BNBLOC_SITES_ORIGIN (a separate https origin serving
# sites and their app API, routed to this container by the edge proxy; a
# malformed value is fatal) the same way; unset means sites are served
# sandboxed from the public origin.
# Browser push reads BNBLOC_WEB_PUSH_VAPID_PUBLIC_KEY / _PRIVATE_KEY /
# _SUBJECT (realtime) the same way; unset means the channel is off.
# The gateway, realtime and room read BNBLOC_SECRET_STORE_KEY (seals component
# settings, endpoint components' credentials and interactive apps' data keys)
# the same way; unset means platform and organization component settings,
# notifier and auth-provider installs, and app data are off.
# Realtime reads its blob store settings (BNBLOC_ATTACHMENTS_DIR or
# BNBLOC_ATTACHMENTS_S3_*, incl. _DIRECT_DOWNLOADS / _PUBLIC_ENDPOINT for
# presigned downloads) straight from the container environment.
# Realtime reads the message search backend (BNBLOC_SEARCH_OPENSEARCH_URL,
# _USERNAME, _PASSWORD, _INDEX_PREFIX; unset URL = MySQL FULLTEXT) straight
# from the container environment; with a URL it probes the cluster at
# startup and, in production, exits when the index is unusable.
# The room service reads BNBLOC_ARTIFACT_MAX_BYTES (one blob artifact
# version's cap; unset = 100 MiB) straight from the container environment;
# blob versions are stored through realtime's blob store over loopback.

# /metrics: the internal services keep it on their loopback-only listeners;
# the gateway (the public listener) serves it only on BNBLOC_METRICS_ADDR.
# All processes share this environment and one network namespace, so the
# value is handed to the gateway alone — exported to every service, all of them
# would try to bind the same address.
gateway_metrics_addr="${BNBLOC_METRICS_ADDR:-:9090}"
unset BNBLOC_METRICS_ADDR

pids=""

# Forward a container stop to every child, then wait for them to drain.
shutdown() {
  echo "aio: received stop; terminating services"
  for p in $pids; do
    kill -TERM "$p" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  exit 0
}
trap shutdown TERM INT

start() {
  name="$1"
  if [ "$name" = gateway ]; then
    BNBLOC_METRICS_ADDR="$gateway_metrics_addr" "/usr/local/bin/bnbloc-${name}" &
  else
    "/usr/local/bin/bnbloc-${name}" &
  fi
  eval "pid_${name}=$!"
  pids="$pids $!"
  echo "aio: started ${name} (pid $!)"
}

# Room first — the others perform membership checks against it. The gateway is
# last so it only fronts traffic once its upstreams are live.
# The organization board's backend (pulse) runs only when its Redis is set: the
# same variable the emitting services read. Without it the board is off and the
# gateway's board routes answer "not deployed", as on any other deployment.
supervised="room activity realtime shareagent relay gateway"
if [ -n "${BNBLOC_PULSE_REDIS_URL:-}" ]; then
  start pulse
  supervised="$supervised pulse"
  export BNBLOC_PULSE_BASE_URL="http://127.0.0.1:8090"
else
  unset BNBLOC_PULSE_BASE_URL
fi
start gateway

echo "aio: all services started; supervising"

# Poll liveness. If any supervised process is gone, stop the rest and exit
# non-zero so the orchestrator restarts the whole container. Polling avoids the
# SIGCHLD races that a trap-based supervisor hits under `set -e`.
while true; do
  sleep 5
  for name in $supervised; do
    eval "pid=\$pid_${name}"
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "aio: ${name} (pid ${pid}) exited; bringing the container down"
      for p in $pids; do
        kill -TERM "$p" 2>/dev/null || true
      done
      wait 2>/dev/null || true
      exit 1
    fi
  done
done
