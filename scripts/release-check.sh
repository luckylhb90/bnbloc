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
# With BNBLOC_RELEASE_SCENARIO=1 each agent also runs the long-task
# scenario (`bnbloc smoke --scenario long-task`: an attachment reaches it,
# it writes a workspace file, and its conversation and file survive
# BNBLOC_RELEASE_BETWEEN — the release step, run between the turns; empty
# = none).
#
# With BNBLOC_RELEASE_TWO_MEMBERS=1 the first agent also runs the
# two-member check (scripts/two-member-check.sh).
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
for agent in ${agents[@]+"${agents[@]}"}; do
  echo "==> bnbloc smoke --agent $agent"
  bnbloc smoke --agent "$agent" --timeout "$timeout" || failed=1
  if [ "${BNBLOC_RELEASE_SCENARIO:-}" = 1 ]; then
    echo "==> bnbloc smoke --agent $agent --scenario long-task"
    between=()
    if [ -n "${BNBLOC_RELEASE_BETWEEN:-}" ]; then between=(--between "$BNBLOC_RELEASE_BETWEEN"); fi
    bnbloc smoke --agent "$agent" --scenario long-task --timeout "$timeout" ${between[@]+"${between[@]}"} || failed=1
  fi
done

# With BNBLOC_RELEASE_TWO_MEMBERS=1 a second member reads the room and
# borrows the first agent (scripts/two-member-check.sh; the teammate's
# provider in BNBLOC_RELEASE_TEAMMATE_AGENT).
if [ "${BNBLOC_RELEASE_TWO_MEMBERS:-}" = 1 ]; then
  if [ -z "${agents[0]+set}" ]; then
    echo "BNBLOC_RELEASE_TWO_MEMBERS=1 needs an agent" >&2
    failed=1
  else
    echo "==> scripts/two-member-check.sh ${agents[0]}"
    "$(dirname "$0")/two-member-check.sh" "${agents[0]}" "${BNBLOC_RELEASE_TEAMMATE_AGENT:-}" || failed=1
  fi
fi

if [ "$failed" -ne 0 ]; then
  echo "release check FAILED" >&2
  exit 1
fi
echo "release check passed (${#agents[@]} agent(s))"
