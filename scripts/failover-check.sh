#!/usr/bin/env bash
# failover-check.sh: load the HA dev stack (compose.ha.yml) and, while the
# load runs, drain one realtime replica, bring it back, kill another, bring
# it back. Prints the load report; a pass is every send stored and every
# delivery arrived (docs/load-testing.md "Failover").
#
#   docker compose -f compose.yml -f compose.ha.yml up -d --build
#   scripts/failover-check.sh [compose project, default: the directory name]
set -euo pipefail
project="${1:-$(basename "$PWD")}"
gateway="${GATEWAY:-http://localhost:7090}"
replica() { docker ps --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.service=realtime" --filter "label=com.docker.compose.container-number=$1" --format '{{.Names}}'; }
r2=$(replica 2); r3=$(replica 3)
[ -n "$r2" ] && [ -n "$r3" ] || { echo "no realtime replicas 2 and 3 in project $project (run with compose.ha.yml)" >&2; exit 2; }
tool=$(mktemp -d)/bnbloc-loadtest
go build -o "$tool" ./cmd/bnbloc-loadtest
ulimit -n 8192 || true
"$tool" --gateway "$gateway" --forwarded-for --members 300 --rooms 30 --channels 3 --rate 50 \
  --duration 120s --ramp-up 10s --settle 10s --drain 15s --progress 10s --json > failover.json &
load=$!
sleep 50; echo "$(date +%T) drain $r2";  docker stop "$r2" >/dev/null
sleep 20; echo "$(date +%T) start $r2";  docker start "$r2" >/dev/null
sleep 25; echo "$(date +%T) crash $r3";  docker kill "$r3" >/dev/null
sleep 15; echo "$(date +%T) start $r3";  docker start "$r3" >/dev/null
wait "$load"
jq -c '{sends:.sends|{ok,attempted,errors},delivery:.delivery|{expected,delivered,lost,p99:.latencyMs.p99},streams:.streams|{disconnects,gaps,connectErrors,openAtEnd}}' failover.json
jq -e '.sends.ok == .sends.attempted and .delivery.lost == 0 and .streams.openAtEnd == .setup.streams' failover.json >/dev/null && echo PASS || { echo FAIL; exit 1; }
