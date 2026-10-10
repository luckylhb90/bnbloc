package notify

import (
	"regexp"
	"strings"
)

// ReplySummaryRunes bounds the reply summary an agent_finished notification
// carries: enough to tell what the agent answered, short enough for a
// messenger preview or a push notification body.
const ReplySummaryRunes = 280

// codeElided replaces a fenced code block in a summary: code is noise in a
// one-paragraph preview and the deep link opens the full reply.
const codeElided = "[code]"

var (
	mdFence      = regexp.MustCompile("(?ms)^[ \t]*(```|~~~)[^\n]*\n.*?(?:^[ \t]*(?:```|~~~)[ \t]*$|\\z)")
	mdImage      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink       = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdRefLink    = regexp.MustCompile(`\[([^\]]+)\]\[[^\]]*\]`)
	mdLinkDef    = regexp.MustCompile(`(?m)^[ \t]*\[[^\]]+\]:[ \t]*\S+.*$`)
	mdInlineCode = regexp.MustCompile("`+([^`]*)`+")
	mdHTML       = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	mdHeading    = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	mdQuote      = regexp.MustCompile(`(?m)^[ \t]*(?:>[ \t]?)+`)
	mdBullet     = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+[.)])[ \t]+(?:\[[ xX]\][ \t]+)?`)
	mdRule       = regexp.MustCompile(`(?m)^[ \t]*(?:[-*_][ \t]*){3,}$`)
	mdTableRule  = regexp.MustCompile(`(?m)^[ \t]*\|?[ \t]*:?-{2,}:?[ \t]*(?:\|[ \t]*:?-{2,}:?[ \t]*)*\|?[ \t]*$`)
	mdStrong     = regexp.MustCompile(`(\*\*|__|~~)(\S(?:.*?\S)?)(\*\*|__|~~)`)
	mdEmphasis   = regexp.MustCompile(`(^|[^\w*])[*_](\S(?:[^*_\n]*?\S)?)[*_]([^\w*]|$)`)
	spaceRun     = regexp.MustCompile(`\s+`)
)

// ReplySummary renders an agent reply (markdown) as a short plain-text
// preview: fenced code blocks are elided, links and images keep their text,
// markup (headings, emphasis, quotes, list markers, tables, HTML) is
// dropped, whitespace collapses to single spaces, and the result is cut at
// maxRunes on a rune boundary with an ellipsis.
func ReplySummary(markdown string, maxRunes int) string {
	s := strings.ReplaceAll(markdown, "\r\n", "\n")
	s = mdFence.ReplaceAllString(s, "\n"+codeElided+"\n")
	s = mdLinkDef.ReplaceAllString(s, "")
	s = mdImage.ReplaceAllString(s, "$1")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdRefLink.ReplaceAllString(s, "$1")
	s = mdInlineCode.ReplaceAllString(s, "$1")
	s = mdHTML.ReplaceAllString(s, "")
	s = mdTableRule.ReplaceAllString(s, "")
	s = mdRule.ReplaceAllString(s, "")
	s = mdHeading.ReplaceAllString(s, "")
	s = mdQuote.ReplaceAllString(s, "")
	s = mdBullet.ReplaceAllString(s, "")
	s = mdStrong.ReplaceAllString(s, "$2")
	// Two passes: adjacent emphasized words share a delimiter context.
	for i := 0; i < 2; i++ {
		s = mdEmphasis.ReplaceAllString(s, "$1$2$3")
	}
	s = strings.ReplaceAll(s, "|", " ")
	s = strings.TrimSpace(spaceRun.ReplaceAllString(s, " "))
	if maxRunes <= 0 {
		return s
	}
	return truncateRunes(s, maxRunes)
}
