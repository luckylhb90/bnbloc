#!/usr/bin/env bash
# failover-check.sh: load the HA dev stack (compose.ha.yml) and, while the
# load runs, take part of it away. Prints the load report; a pass is every
# send stored, every delivery arrived and every stream open at the end
# (docs/load-testing.md "Failover").
#
#   docker compose -f compose.yml -f compose.ha.yml up -d --build
#   scripts/failover-check.sh [drill] [compose project, default: the directory name]
#
# Drills:
#   replicas  drain one realtime replica, start it, kill another, start it
#   rolling   a rolling deploy: drain and start each realtime replica in turn
#   scale     scale realtime out from 3 to 5 replicas, then back to 3
#   redis     restart the Redis the replicas' bus runs on (its failover)
set -euo pipefail
drill="${1:-replicas}"
project="${2:-$(basename "$PWD")}"
gateway="${GATEWAY:-http://localhost:7090}"
compose=(docker compose -p "$project" -f compose.yml -f compose.ha.yml ${COMPOSE_EXTRA:-})
replica() { docker ps --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.service=$2" --filter "label=com.docker.compose.container-number=$1" --format '{{.Names}}'; }
r1=$(replica 1 realtime); r2=$(replica 2 realtime); r3=$(replica 3 realtime)
[ -n "$r1" ] && [ -n "$r2" ] && [ -n "$r3" ] || { echo "no realtime replicas 1 to 3 in project $project (run with compose.ha.yml)" >&2; exit 2; }
tool=$(mktemp -d)/bnbloc-loadtest
go build -o "$tool" ./cmd/bnbloc-loadtest
ulimit -n 8192 || true
"$tool" --gateway "$gateway" --forwarded-for --members 300 --rooms 30 --channels 3 --rate 50 \
  --duration 120s --ramp-up 10s --settle 10s --drain 15s --progress 10s --json > failover.json &
load=$!
at() { echo "$(date +%T) $*"; }
case "$drill" in
  replicas)
    sleep 50; at "drain $r2"; docker stop "$r2" >/dev/null
    sleep 20; at "start $r2"; docker start "$r2" >/dev/null
    sleep 25; at "crash $r3"; docker kill "$r3" >/dev/null
    sleep 15; at "start $r3"; docker start "$r3" >/dev/null ;;
  rolling)
    sleep 40
    for r in "$r1" "$r2" "$r3"; do
      at "drain $r"; docker stop "$r" >/dev/null
      sleep 3; at "start $r"; docker start "$r" >/dev/null
      sleep 15
    done ;;
  scale)
    sleep 40; at "scale realtime to 5"; "${compose[@]}" up -d --no-recreate --no-build --scale realtime=5 realtime >/dev/null 2>&1
    sleep 35; at "scale realtime to 3"; "${compose[@]}" up -d --no-recreate --no-build --scale realtime=3 realtime >/dev/null 2>&1 ;;
  redis)
    redis=$(replica 1 redis)
    [ -n "$redis" ] || { echo "no redis in project $project" >&2; kill "$load"; exit 2; }
    sleep 50; at "restart $redis"; docker restart -t 0 "$redis" >/dev/null ;;
  *) echo "unknown drill $drill (replicas, rolling, scale, redis)" >&2; kill "$load"; exit 2 ;;
esac
wait "$load"
jq -c '{sends:.sends|{ok,attempted,errors},delivery:.delivery|{expected,delivered,lost,p99:.latencyMs.p99},streams:.streams|{disconnects,gaps,connectErrors,openAtEnd}}' failover.json
jq -e '.sends.ok == .sends.attempted and .delivery.lost == 0 and .streams.openAtEnd == .setup.streams' failover.json >/dev/null && echo PASS || { echo FAIL; exit 1; }
