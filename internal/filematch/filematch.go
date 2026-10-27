// Package filematch is the one validator of a file match spec
// (wire.FileMatch: MIME types and extensions), the vocabulary every
// declaration of "the files this applies to" shares: a previewer package's
// match and a prompt-pack prompt's files
// (docs/prd/2026-10-14-file-prompts-and-compare.md rule 1). The web scores
// a match against a file in web/src/lib/fileMatch.ts; Claims below is only the
// server's yes or no, for choosing candidates.
package filematch

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MaxEntries bounds each list of a match.
const MaxEntries = 32

var (
	// A lower-case type/subtype or type/* (never */*), no parameters.
	mimeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,62}/(\*|[a-z0-9][a-z0-9!#$&^_.+-]{0,126})$`)
	// A lower-case .ext of up to three parts (".atlas.json").
	extensionRE = regexp.MustCompile(`^(\.[a-z0-9][a-z0-9_-]{0,15}){1,3}$`)
)

// Validate checks m, naming it field in errors ("previewer.match").
func Validate(m wire.FileMatch, field string) error {
	if len(m.MimeTypes)+len(m.Extensions) == 0 {
		return fmt.Errorf("%s requires at least one MIME type or extension", field)
	}
	if len(m.MimeTypes) > MaxEntries || len(m.Extensions) > MaxEntries {
		return fmt.Errorf("%s lists hold at most %d entries", field, MaxEntries)
	}
	seen := map[string]bool{}
	for _, mt := range m.MimeTypes {
		if !mimeRE.MatchString(mt) {
			return fmt.Errorf("%s.mimeTypes: %q must be a lower-case type/subtype or type/*", field, mt)
		}
		if seen[mt] {
			return fmt.Errorf("%s.mimeTypes: duplicate %q", field, mt)
		}
		seen[mt] = true
	}
	for _, ext := range m.Extensions {
		if !extensionRE.MatchString(ext) {
			return fmt.Errorf("%s.extensions: %q must be a lower-case .ext (up to three parts)", field, ext)
		}
		if seen[ext] {
			return fmt.Errorf("%s.extensions: duplicate %q", field, ext)
		}
		seen[ext] = true
	}
	return nil
}

// Claims reports whether m claims a file of this name and media type: one of
// its extensions matches the end of the name (".atlas.json" claims
// "ship.atlas.json" and ".json" claims it too), or one of its MIME types is
// the file's (exact, or type/* for its top-level type), compared lower-case
// and without parameters. An empty name or type claims nothing by that route.
// It is the server's candidate filter, so a match that the web scores more
// finely (specificity, rank) still lists its previewer.
func Claims(m wire.FileMatch, name, mimeType string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, ext := range m.Extensions {
		if name != "" && ext != "" && strings.HasSuffix(name, strings.ToLower(ext)) {
			return true
		}
	}
	mt := strings.ToLower(strings.TrimSpace(mimeType))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	if mt == "" {
		return false
	}
	top, _, _ := strings.Cut(mt, "/")
	for _, claimed := range m.MimeTypes {
		claimed = strings.ToLower(claimed)
		if claimed == mt || (claimed == top+"/*" && top != "") {
			return true
		}
	}
	return false
}
