#!/usr/bin/env bash
# Release check: the stack answers and every given agent replies.
#
#   scripts/release-check.sh [agent ...]
#
# Runs `bnbloc smoke` (sign-in, versions, a room, chat, history) and then
# `bnbloc smoke --agent <agent>` for each agent: the arguments, else the
# space-separated BNBLOC_RELEASE_AGENTS (core/provider of your own agents,
# e.g. "acp/codex local/claude"). Non-zero when any check fails.
#
# A core, desktop or service release runs it BEFORE and AFTER the release
# (AGENTS.md, "Releases"): a release that breaks a working agent is caught
# by the operator, not by a member. Needs a signed-in bnbloc CLI and, for
# --agent, the agents' bridge online.
set -uo pipefail

agents=("$@")
if [ ${#agents[@]} -eq 0 ] && [ -n "${BNBLOC_RELEASE_AGENTS:-}" ]; then
  read -r -a agents <<<"$BNBLOC_RELEASE_AGENTS"
fi
timeout="${BNBLOC_RELEASE_AGENT_TIMEOUT:-4m}"

failed=0
echo "==> bnbloc smoke"
bnbloc smoke || failed=1
for agent in "${agents[@]}"; do
  echo "==> bnbloc smoke --agent $agent"
  bnbloc smoke --agent "$agent" --timeout "$timeout" || failed=1
done

if [ "$failed" -ne 0 ]; then
  echo "release check FAILED" >&2
  exit 1
fi
echo "release check passed (${#agents[@]} agent(s))"
