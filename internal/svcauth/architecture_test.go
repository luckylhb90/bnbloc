package svcauth

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// scanSources returns "<file>: <what>" for every non-test Go file under
// services/, internal/ and cmd/ (outside internal/svcauth and allow) whose
// source matches a rule.
func scanSources(t *testing.T, rules map[string]*regexp.Regexp, allow map[string]string) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	var problems []string
	for _, dir := range []string{"services", "internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/svcauth/") {
				return nil
			}
			for prefix := range allow {
				if strings.HasPrefix(rel, prefix) {
					return nil
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for what, re := range rules {
				if re.Match(raw) {
					problems = append(problems, rel+": "+what)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(problems)
	return problems
}

// TestSignedRequestsUseNewRequest: the internal identity assertion (the
// subject, its tenant, its org role and the signature over them, set in
// that order) has one writer, Key.Assert, reached through NewRequest or —
// for the gateway's forwarded request — Assert itself. A hand-set
// X-Bnbloc-User or X-Bnbloc-Tenant drifts: the tenant was set only when
// non-empty in one caller, never in another, after the signature in a
// third (2026-10-12 review: eight gateway copies, six in the services).
func TestSignedRequestsUseNewRequest(t *testing.T) {
	rules := map[string]*regexp.Regexp{
		"sets X-Bnbloc-User by hand":   regexp.MustCompile(`Header\.Set\((svcauth\.HeaderUser|"X-Bnbloc-User")`),
		"sets X-Bnbloc-Tenant by hand": regexp.MustCompile(`Header\.Set\((svcauth\.HeaderTenant|"X-Bnbloc-Tenant")`),
		"sets X-Bnbloc-Role by hand":   regexp.MustCompile(`Header\.Set\((svcauth\.HeaderRole|"X-Bnbloc-Role")`),
		"sets the signature by hand":   regexp.MustCompile(`Header\.Set\(svcauth\.Header(Sig|Ts)`),
	}
	if problems := scanSources(t, rules, nil); len(problems) > 0 {
		t.Fatalf("build internal requests with svcauth.NewRequest (or Key.Assert):\n  %s", strings.Join(problems, "\n  "))
	}
}

// TestSubjectReadOnlyViaSvcauth: a handler reads its verified caller with
// svcauth.Subject and its org role with svcauth.Role, never the raw header
// (the copies trimmed differently and one answered 403 for a missing
// subject where the others answered 401).
func TestSubjectReadOnlyViaSvcauth(t *testing.T) {
	rules := map[string]*regexp.Regexp{
		"reads X-Bnbloc-User": regexp.MustCompile(`Header\.Get\((svcauth\.HeaderUser|"X-Bnbloc-User")`),
		"reads X-Bnbloc-Role": regexp.MustCompile(`Header\.Get\((svcauth\.HeaderRole|"X-Bnbloc-Role")`),
	}
	allow := map[string]string{
		"internal/obs/": "the request log line names the asserted subject; obs sits below svcauth (svcauth → httpx → obs)",
	}
	if problems := scanSources(t, rules, allow); len(problems) > 0 {
		t.Fatalf("read the caller with svcauth.Subject / svcauth.Role:\n  %s", strings.Join(problems, "\n  "))
	}
}
