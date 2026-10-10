package notify

import "strings"

// maxMentionCandidates bounds how many @-tokens of one message are checked
// against room membership (each check may be a membership lookup).
const maxMentionCandidates = 20

// MentionCandidates extracts the member ids a chat text @-mentions. The web
// composer inserts a picked member as "@<memberId> " (member ids may be
// emails, so "@" and "." are part of the token); agent handles are
// "@owner/provider" and are not member mentions, so tokens with "/" are
// skipped. Trailing sentence punctuation is trimmed. Candidates are
// de-duplicated (case-insensitively) and capped; membership is the caller's
// check — a candidate is only a mention if it names a room member.
func MentionCandidates(text string) []string {
	seen := map[string]bool{}
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '@' || (i > 0 && !isSpace(text[i-1])) {
			continue
		}
		j := i + 1
		for j < len(text) && !isSpace(text[j]) {
			j++
		}
		tok := strings.TrimRight(text[i+1:j], ".,;:!?)]}>\"'")
		i = j - 1
		if tok == "" || strings.Contains(tok, "/") {
			continue
		}
		key := strings.ToLower(tok)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tok)
		if len(out) == maxMentionCandidates {
			break
		}
	}
	return out
}

func isSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}
