// Package sharecard is the one owner of a share's card: what a shared item
// becomes before a share-target component formats it for its app (title,
// plain-text summary, link back). Item text is untrusted: the summary is
// plain text of bounded length, whatever the item held.
package sharecard

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Card is a shared item as a neutral card (wire.ShareCard).
type Card = wire.ShareCard

var (
	markdownLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	lineMarker   = regexp.MustCompile(`(?m)^\s{0,3}(?:#{1,6}|>|[-*+]|\d+\.)\s+`)
	markup       = strings.NewReplacer("*", "", "`", "", "~~", "")
)

// Plain is text as one line of plain text: markdown links keep their text,
// emphasis, code and heading/quote/list markers are dropped, control
// characters and runs of whitespace become one space.
func Plain(s string) string {
	s = markdownLink.ReplaceAllString(s, "$1")
	s = lineMarker.ReplaceAllString(s, "")
	s = markup.Replace(s)
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

// cut is s limited to n runes, ending in an ellipsis when cut.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// Title is a card title: plain text within wire.MaxShareTitleRunes.
func Title(s string) string { return cut(Plain(s), wire.MaxShareTitleRunes) }

// Summary is a card summary: plain text within wire.MaxShareSummaryRunes.
func Summary(s string) string { return cut(Plain(s), wire.MaxShareSummaryRunes) }

// New builds a card from an item's raw title, raw summary text and link.
func New(kind, title, summary, url string) Card {
	return Card{Kind: kind, Title: Title(title), Summary: Summary(summary), URL: url}
}
