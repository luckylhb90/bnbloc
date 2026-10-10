package sync

import (
	"bytes"
	"strings"
)

// isText reports whether data looks like UTF-8 text safe for line merging.
// Binary content (NUL byte or invalid UTF-8) is never line-merged.
func isText(data []byte) bool {
	if bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	return len(data) == 0 || len(bytes.ToValidUTF8(data, nil)) == len(data)
}

// threeWayMerge performs a diff3-style line merge of local and remote against a
// common ancestor. It returns the merged text and whether the merge was clean
// (no conflicting hunks). On conflict it still returns a best-effort result with
// conflict markers, and ok=false so the caller preserves both versions instead.
func threeWayMerge(ancestor, local, remote []byte) (merged []byte, ok bool) {
	if !isText(ancestor) || !isText(local) || !isText(remote) {
		return nil, false
	}
	a := splitLines(string(ancestor))
	l := splitLines(string(local))
	r := splitLines(string(remote))

	// Longest-common-subsequence alignment of ancestor->local and ancestor->remote,
	// then walk all three, emitting local/remote changes and flagging overlaps.
	lMatch := lcsMatch(a, l) // ancestor index -> local index (or -1)
	rMatch := lcsMatch(a, r)

	var out []string
	clean := true
	ai, li, ri := 0, 0, 0
	for ai < len(a) || li < len(l) || ri < len(r) {
		// Emit local-only insertions that precede the next ancestor anchor.
		lChanged := regionChanged(a, l, lMatch, ai, li)
		rChanged := regionChanged(a, r, rMatch, ai, ri)

		if lChanged || rChanged {
			// Gather the changed region on each side up to the next common anchor.
			aNext := nextAnchor(ai, lMatch, rMatch, len(a))
			lNext := sideNext(ai, aNext, lMatch, li, len(l))
			rNext := sideNext(ai, aNext, rMatch, ri, len(r))
			localHunk := l[li:lNext]
			remoteHunk := r[ri:rNext]
			base := a[ai:aNext]

			switch {
			case equalLines(localHunk, base): // only remote changed
				out = append(out, remoteHunk...)
			case equalLines(remoteHunk, base): // only local changed
				out = append(out, localHunk...)
			case equalLines(localHunk, remoteHunk): // same change both sides
				out = append(out, localHunk...)
			default: // true conflict
				clean = false
				out = append(out, "<<<<<<< local")
				out = append(out, localHunk...)
				out = append(out, "=======")
				out = append(out, remoteHunk...)
				out = append(out, ">>>>>>> remote")
			}
			ai, li, ri = aNext, lNext, rNext
			continue
		}
		// Unchanged anchor line: advance all three.
		if ai < len(a) {
			out = append(out, a[ai])
		}
		ai++
		li++
		ri++
	}
	return []byte(joinLines(out)), clean
}

// --- line helpers ---

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lcsMatch returns, for each index in a, the matched index in b under a longest
// common subsequence, or -1 when a[i] has no match.
func lcsMatch(a, b []string) []int {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	match := make([]int, n)
	for i := range match {
		match[i] = -1
	}
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			match[i] = j
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return match
}

// regionChanged reports whether the side diverged from ancestor at position ai.
func regionChanged(a, side []string, match []int, ai, si int) bool {
	if ai >= len(a) {
		return si < len(side) // trailing insertions on the side
	}
	if match[ai] == si {
		return false // ancestor line ai maps exactly to side position si: unchanged
	}
	return true
}

// nextAnchor finds the next ancestor index that both sides keep unchanged.
func nextAnchor(ai int, lMatch, rMatch []int, aLen int) int {
	for k := ai + 1; k < aLen; k++ {
		if lMatch[k] >= 0 && rMatch[k] >= 0 {
			return k
		}
	}
	return aLen
}

// sideNext returns the side index aligned to ancestor anchor aNext.
func sideNext(ai, aNext int, match []int, si, sideLen int) int {
	if aNext < len(match) && match[aNext] >= 0 {
		return match[aNext]
	}
	return sideLen
}
