// Package filematch is the one validator of a file match spec
// (wire.FileMatch: MIME types and extensions), the vocabulary every
// declaration of "the files this applies to" shares: a previewer package's
// match and a prompt-pack prompt's files
// (docs/prd/2026-10-14-file-prompts-and-compare.md rule 1). The web scores
// a match against a file in web/src/lib/fileMatch.ts.
package filematch

import (
	"fmt"
	"regexp"

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
