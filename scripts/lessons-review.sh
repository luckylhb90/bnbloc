#!/usr/bin/env bash
# The daily review's digest (LESSONS.md "When it is written"): what landed,
# what was reworked, which bug records were written and which lessons are
# due for promotion. It prints facts for the reviewing agent; the agent
# writes the lessons.
#
#   scripts/lessons-review.sh                    the last day
#   scripts/lessons-review.sh --since 2026-10-13
#   scripts/lessons-review.sh --repo <dir>       another checkout with a LESSONS.md
#
# Rework: a commit whose subject reads as a fix (fix, revert, again,
# regress, broke, follow-up) that touches a file a commit of the three
# days before it also changed.
set -euo pipefail

since="1 day ago"
repo="$(cd "$(dirname "$0")/.." && pwd)"
while [ $# -gt 0 ]; do
  case "$1" in
    --since) since="$2"; shift 2 ;;
    --repo) repo="$2"; shift 2 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
cd "$repo"
# git reads a bare date as a time of day: name midnight.
[[ "$since" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] && since="$since 00:00"

echo "# Review digest: $(basename "$repo") since $since"
echo
echo "## Commits"
git log --no-merges --since="$since" --reverse --format='- %h %ad %s' --date=format:'%m-%d %H:%M'
echo
echo "## Reworks (a fix touching what the three days before changed)"
found=0
while IFS=$'\t' read -r sha when subject; do
  shopt -s nocasematch
  if [[ ! "$subject" =~ (^|[^a-z])(fix|fixes|fixed|revert|again|regress|broke|follow-up)([^a-z]|$) ]]; then
    shopt -u nocasematch
    continue
  fi
  shopt -u nocasematch
  earlier=$(git log --no-merges --format='%h %s' --since="$(date -r "$when" -v-3d '+%Y-%m-%dT%H:%M:%S' 2>/dev/null || date -d "@$((when - 259200))" '+%Y-%m-%dT%H:%M:%S')" "$sha^" -- $(git show --format= --name-only "$sha" | grep -v '^docs/\|^LESSONS.md$' || true) 2>/dev/null | head -3 || true)
  if [ -n "$earlier" ]; then
    found=1
    echo "- $sha $subject"
    echo "$earlier" | sed 's/^/    after /'
  fi
done < <(git log --no-merges --since="$since" --format=$'%h\t%at\t%s')
[ "$found" = 1 ] || echo "- none found"
echo
echo "## Bug records and PRDs written or changed"
git log --no-merges --since="$since" --name-only --format= -- docs/bugs docs/prd | sort -u | sed 's/^/- /'
echo
echo "## Lessons due for promotion (seen on 3+ distinct days)"
if [ -f LESSONS.md ]; then
  awk '
    /^### / { title = substr($0, 5) }
    /^## Promoted/ { done = 1 }
    !done && /^Seen [0-9]+×/ {
      n = split($0, parts, /[(), ]+/); days = 0
      for (i = 1; i <= n; i++) if (parts[i] ~ /^[0-9]{4}-[0-9]{2}-[0-9]{2}$/) seen[parts[i]] = 1
      for (d in seen) days++
      if (days >= 3) printf "- %s (%d days)\n", title, days
      delete seen
    }
  ' LESSONS.md | grep . || echo "- none"
else
  echo "- no LESSONS.md in $repo"
fi
