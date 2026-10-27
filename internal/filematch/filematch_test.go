package filematch

import (
	"regexp"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestValidate(t *testing.T) {
	ok := []wire.FileMatch{
		{MimeTypes: []string{"image/png", "image/*"}},
		{Extensions: []string{".pptx", ".atlas.json"}},
	}
	for _, m := range ok {
		if err := Validate(m, "files"); err != nil {
			t.Errorf("%+v: %v", m, err)
		}
	}
	bad := map[string]wire.FileMatch{
		"requires at least one":  {},
		"lower-case type":        {MimeTypes: []string{"*/*"}},
		"lower-case type/":       {MimeTypes: []string{"Image/PNG"}},
		"must be a lower-case .": {Extensions: []string{"pptx"}},
		"duplicate":              {Extensions: []string{".md", ".md"}},
		"at most":                {Extensions: make([]string, MaxEntries+1)},
	}
	for want, m := range bad {
		err := Validate(m, "prompts[0].files")
		if err == nil || !strings.Contains(err.Error(), want) || !strings.HasPrefix(err.Error(), "prompts[0].files") {
			t.Errorf("%+v: got %v, want an error naming the field and %q", m, err, want)
		}
	}
}

func TestClaims(t *testing.T) {
	m := wire.FileMatch{MimeTypes: []string{"image/*", "application/vnd.x+json"}, Extensions: []string{".atlas.json", ".drawio"}}
	for _, c := range []struct {
		name, mime string
		want       bool
	}{
		{"ship.atlas.json", "application/json", true}, // a multi-part extension, by the end of the name
		{"SHIP.ATLAS.JSON", "", true},                 // case-insensitive
		{"plan.drawio", "text/plain", true},           // a plain extension
		{"plan.drawio.bak", "text/plain", false},      // the end of the name, not the middle
		{"atlas.json", "application/json", false},     // ".atlas.json" needs the dot
		{"photo", "image/png", true},                  // type/* by the file's top-level type
		{"photo", "IMAGE/PNG; charset=binary", true},  // lower-case, parameters dropped
		{"data", "application/vnd.x+json", true},      // exact
		{"data", "application/vnd.y+json", false},
		{"data", "", false}, // nothing known claims nothing
		{"", "", false},
		{"note.md", "text/markdown", false},
	} {
		if got := Claims(m, c.name, c.mime); got != c.want {
			t.Errorf("Claims(%q, %q) = %v, want %v", c.name, c.mime, got, c.want)
		}
	}
	if Claims(wire.FileMatch{}, "a.png", "image/png") {
		t.Error("an empty match claims a file")
	}
}

// One owner for "does this apply to this file"
// (docs/prd/2026-10-14-file-prompts-and-compare.md rule 1): Go validates a
// match only here (no second MIME-type pattern), and the web scores one only
// in lib/fileMatch.ts (no second matchSpecificity).
func TestOneFileMatchOwner(t *testing.T) {
	goMime := regexp.MustCompile("regexp\\.MustCompile\\(`\\^\\[a-z0-9\\]\\[a-z0-9!#")
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(path string, src []byte) {
		if path == "internal/filematch/filematch.go" {
			return
		}
		if goMime.Match(src) {
			t.Errorf("%s declares a MIME-type pattern: validate a wire.FileMatch with filematch.Validate", path)
		}
	})
	tsScorer := regexp.MustCompile(`function\s+matchSpecificity\b|=\s*matchSpecificity\s*=`)
	archtest.Scan(t, archtest.Opts{Lang: archtest.TS, Roots: []string{"web/src"}}, func(path string, src []byte) {
		if path == "web/src/lib/fileMatch.ts" {
			return
		}
		if tsScorer.Match(src) {
			t.Errorf("%s scores file matches: use matchSpecificity from lib/fileMatch.ts", path)
		}
	})
}
