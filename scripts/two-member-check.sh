#!/usr/bin/env bash
# Two-member check: a second member reads what the first did in a room and
# borrows the first member's agent (`bnbloc smoke --scenario two-members`).
#
#   scripts/two-member-check.sh <A's agent> <teammate provider>
#
# A is the signed-in bnbloc CLI's member and <A's agent> one of their agents
# (as for `bnbloc smoke --agent`, e.g. acme/claude). The teammate B is a
# service subject (BNBLOC_RELEASE_TEAMMATE, default svc:release-teammate)
# this script makes for the run: a 1-day API token (needs a super-admin
# sign-in), exchanged for a session that drives a bridge of B's own on this
# machine. The bridge serves BNBLOC_RELEASE_TEAMMATE_CORES (default acp) at
# BNBLOC_RELEASE_TEAMMATE_URL (the first core's endpoint, default
# http://127.0.0.1:7341); <teammate provider> is B's agent there. The
# scenario waits for that agent to come online.
#
# Arguments fall back to BNBLOC_RELEASE_TWO_MEMBERS_AGENT and
# BNBLOC_RELEASE_TEAMMATE_AGENT. BNBLOC_BIN names the CLI (default bnbloc),
# BNBLOC_BRIDGE_BIN the bridge (default: built from services/bridge/cmd),
# BNBLOC_RELEASE_AGENT_TIMEOUT the per-turn bound (default 6m).
#
# Whatever happens, the bridge is stopped, the token revoked and the
# temporary files removed; the token is kept in a 0600 file and never
# printed. A failed scenario keeps its room for debugging (as smoke does).
# Needs curl and jq. Non-zero when any check fails.
set -uo pipefail

agent="${1:-${BNBLOC_RELEASE_TWO_MEMBERS_AGENT:-}}"
mate_agent="${2:-${BNBLOC_RELEASE_TEAMMATE_AGENT:-}}"
if [ -z "$agent" ] || [ -z "$mate_agent" ]; then
  echo "usage: $0 <A's agent> <teammate provider>" >&2
  exit 2
fi
bnbloc="${BNBLOC_BIN:-bnbloc}"
mate="${BNBLOC_RELEASE_TEAMMATE:-svc:release-teammate}"
cores="${BNBLOC_RELEASE_TEAMMATE_CORES:-acp}"
core_url="${BNBLOC_RELEASE_TEAMMATE_URL:-http://127.0.0.1:7341}"
timeout="${BNBLOC_RELEASE_AGENT_TIMEOUT:-6m}"
root="$(cd "$(dirname "$0")/.." && pwd)"

umask 077
tmp="$(mktemp -d "${TMPDIR:-/tmp}/two-member-check.XXXXXX")" || exit 1
chmod 700 "$tmp"
token_id=""
bridge_pid=""

cleanup() {
  if [ -n "$bridge_pid" ]; then
    kill "$bridge_pid" 2>/dev/null
    wait "$bridge_pid" 2>/dev/null
    echo "==> bridge stopped"
  fi
  if [ -n "$token_id" ]; then
    "$bnbloc" api-tokens revoke "$token_id" || echo "WARNING: revoke API token $token_id by hand" >&2
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

fail() {
  echo "two-member check: $*" >&2
  exit 1
}

gateway="$("$bnbloc" whoami | awk '$1 == "gateway" { print $2 }')"
[ -n "$gateway" ] || fail "no signed-in gateway (bnbloc login)"

echo "==> API token for $mate"
"$bnbloc" api-tokens create --label "two-member check" --subject "$mate" --ttl-days 1 >"$tmp/create" ||
  fail "api-tokens create failed (needs a super-admin sign-in)"
token_id="$(awk '$1 == "id" { print $2 }' "$tmp/create")"
grep -E '^bnbloc_at_' "$tmp/create" | head -n 1 | tr -d '\n' >"$tmp/token"
rm -f "$tmp/create"
[ -n "$token_id" ] && [ -s "$tmp/token" ] || fail "api-tokens create printed no token"

jq -n --rawfile t "$tmp/token" '{token: $t}' >"$tmp/exchange" || fail "jq failed"
curl -fsS -X POST -H 'Content-Type: application/json' --data-binary "@$tmp/exchange" \
  "$gateway/v1/auth/api-token" >"$tmp/session" || fail "exchanging the teammate's token failed"
tenant="$(jq -r '.tenant // ""' "$tmp/session")"
jq -r '.sessionToken // ""' "$tmp/session" | tr -d '\n' >"$tmp/session-token"
[ -s "$tmp/session-token" ] || fail "the token exchange returned no session"

bridge="${BNBLOC_BRIDGE_BIN:-}"
if [ -z "$bridge" ]; then
  echo "==> building the bridge"
  bridge="$tmp/bnbloc-bridge"
  (cd "$root" && go build -o "$bridge" ./services/bridge/cmd) || fail "building the bridge failed"
fi

echo "==> bridge for $mate ($cores at $core_url)"
first_core="$(printf '%s' "${cores%%,*}" | tr '[:lower:]-' '[:upper:]_')"
mkdir -p "$tmp/state"
(
  BNBLOC_GATEWAY_TOKEN="$(cat "$tmp/session-token")"
  export BNBLOC_GATEWAY_TOKEN
  export BNBLOC_GATEWAY_BASE_URL="$gateway"
  export BNBLOC_BRIDGE_OWNER="$mate"
  export BNBLOC_BRIDGE_TENANT="$tenant"
  export BNBLOC_BRIDGE_ID="two-member-check-$$"
  export BNBLOC_CORES="$cores"
  export "BNBLOC_CORE_${first_core}_URL=$core_url"
  export BNBLOC_BRIDGE_STATE_DIR="$tmp/state"
  exec "$bridge"
) >"$tmp/bridge.log" 2>&1 &
bridge_pid=$!
sleep 2
kill -0 "$bridge_pid" 2>/dev/null || {
  tail -n 20 "$tmp/bridge.log" >&2
  bridge_pid=""
  fail "the bridge exited"
}

echo "==> bnbloc smoke --agent $agent --scenario two-members --teammate-agent $mate_agent"
BNBLOC_TEAMMATE_API_TOKEN="$(cat "$tmp/token")" \
  "$bnbloc" smoke --agent "$agent" --scenario two-members --teammate-agent "$mate_agent" --timeout "$timeout"
status=$?
if [ "$status" -ne 0 ]; then
  echo "==> bridge log (last 30 lines)"
  tail -n 30 "$tmp/bridge.log"
  echo "two-member check FAILED" >&2
  exit 1
fi
echo "two-member check passed"
