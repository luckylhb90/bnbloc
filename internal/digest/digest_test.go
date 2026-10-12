package digest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

func TestDigestRule(t *testing.T) {
	sha := Of([]byte("hello"))
	if sha != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" || !Valid(sha) {
		t.Fatalf("Of = %q", sha)
	}
	for _, bad := range []string{"", strings.ToUpper(sha), sha[:63], sha + "0", strings.Replace(sha, "2", "g", 1), " " + sha[1:]} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
		if ValidKey(KeyPrefix + bad) {
			t.Errorf("ValidKey(sha256/%q) = true", bad)
		}
	}
	if got, ok := ParseKey(Key(sha)); !ok || got != sha {
		t.Fatalf("ParseKey(Key) = %q, %v", got, ok)
	}
	for _, bad := range []string{sha, "SHA256/" + sha, "sha256:" + sha, "/sha256/" + sha, Key(sha) + "/x"} {
		if _, ok := ParseKey(bad); ok {
			t.Errorf("ParseKey(%q) ok", bad)
		}
	}
	if !regexp.MustCompile(KeyPattern).MatchString("https://gw.example.com/v1/artifacts/"+Key(sha)) ||
		regexp.MustCompile(KeyPattern).MatchString(KeyPrefix+strings.ToUpper(sha)) {
		t.Fatal("KeyPattern does not match exactly the canonical keys")
	}
}

// The digest rule has one owner: no other non-test Go file spells the key
// prefix, a 64-hex pattern or length, or compares digests case-blind.
func TestDigestRuleSingleOwner(t *testing.T) {
	lenRule := regexp.MustCompile(`(?i)len\([^)]*(sha|hash|digest|hex)[^)]*\)\s*[!=<>]=?\s*64\b`)
	foldRule := regexp.MustCompile(`(?i)EqualFold\([^)]*(sha|digest|pin)`)
	fset := token.NewFileSet()
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(rel string, src []byte) {
		if path.Dir(rel) == "internal/digest" {
			return
		}
		f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if strings.Contains(v, KeyPrefix) || strings.Contains(v, "{64}") {
				t.Errorf("%s:%d spells the digest rule (%q): use internal/digest", rel, fset.Position(lit.Pos()).Line, v)
			}
			return true
		})
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if lenRule.MatchString(line) || foldRule.MatchString(line) {
				t.Errorf("%s:%d checks or compares a digest itself (%s): use internal/digest (exact, lower case)", rel, i+1, strings.TrimSpace(line))
			}
		}
	})
}
