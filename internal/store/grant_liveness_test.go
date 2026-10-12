package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestGrantLivenessOwned: whether a grant is in force has one answer,
// Grant.Live (and its SQL twin in the MySQL reservation and quota raise).
// Six copies of "not revoked and not expired" once lived in the services,
// and one of them (the platform reserve path) used a different rule. The
// scan fails on a liveness test written out over the grant's fields
// anywhere outside this package: a nil test of RevokedAtMs, a comparison
// against a dereferenced ExpiresAtMs, or the `ExpiresAtMs != nil &&` guard.
func TestGrantLivenessOwned(t *testing.T) {
	root := filepath.Join("..", "..")
	liveness := regexp.MustCompile(`\.RevokedAtMs\s*(!=|==)\s*nil|[<>]=?\s*\*[A-Za-z_][\w.]*\.ExpiresAtMs|\.ExpiresAtMs\s*!=\s*nil\s*&&`)
	var problems []string
	for _, dir := range []string{"services", "internal", "core", "cmd", "adapter"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/store/") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if liveness.MatchString(line) && !strings.HasPrefix(strings.TrimSpace(line), "//") {
					problems = append(problems, rel+":"+strconv.Itoa(i+1))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("grant liveness re-derived (use store.Grant.Live, or the policy-aware usableGrant in shareagent):\n  %s", strings.Join(problems, "\n  "))
	}
}

// Live is "not revoked, and not at or past the expiry".
func TestGrantLive(t *testing.T) {
	at := int64(100)
	exp := func(v int64) *int64 { return &v }
	for _, c := range []struct {
		g    Grant
		want bool
	}{
		{Grant{}, true},
		{Grant{ExpiresAtMs: exp(101)}, true},
		{Grant{ExpiresAtMs: exp(100)}, false},
		{Grant{RevokedAtMs: exp(50)}, false},
	} {
		if got := c.g.Live(at); got != c.want {
			t.Errorf("%+v.Live(%d) = %v, want %v", c.g, at, got, c.want)
		}
	}
}
