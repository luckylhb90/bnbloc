package extpipe

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

// TestVisibilityOwned: "may member M use component C" (and its tenant-only
// half, "does record R reach tenant T") is MemberVisibility /
// MemberVisibilityWith / TenantDistribution, never a rollout comparison or
// a tenant-scope test written out beside it (2026-10-12 review: realtime's
// plugin streams, the notifier directory and HTTP routes, the gateway's
// notifier replies and its catalog re-derived it). The scan fails on a
// branch on a normalized rollout or a direct AllowsTenant call outside this
// package; an admin writer validating a requested rollout is allowlisted
// with its reason.
func TestVisibilityOwned(t *testing.T) {
	root := filepath.Join("..", "..")
	rederived := regexp.MustCompile(`NormalizeRollout\([^)]*\)\s*(==|!=)|switch\s+[\w.]*NormalizeRollout\(|\.AllowsTenant\(`)
	allowed := map[string]string{
		"services/gateway/content_platform.go": "installPlatformContent validates the rollout an admin requests for a signed content package (a write, not a visibility read)",
	}
	var problems []string
	for _, dir := range []string{"services", "internal", "core", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/extpipe/") || allowed[rel] != "" {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if rederived.MatchString(line) && !strings.HasPrefix(strings.TrimSpace(line), "//") {
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
		t.Fatalf("component visibility re-derived (use extpipe.MemberVisibility / MemberVisibilityWith / TenantDistribution):\n  %s", strings.Join(problems, "\n  "))
	}
}

// MemberVisibilityWith reads the install only for an opt-in component the
// member may see; TenantDistribution is the tenant half.
func TestMemberVisibilityWithAndTenantDistribution(t *testing.T) {
	maya := Viewer{Subject: "maya", Tenant: "acme"}
	reads := 0
	installedAt := func(at int64) func() int64 { return func() int64 { reads++; return at } }
	auto := Record{Rollout: RolloutAuto}
	optin := Record{Rollout: RolloutOptIn}
	off := Record{Rollout: RolloutOff}
	scoped := Record{Rollout: RolloutAuto, EnabledTenants: []string{"globex"}}

	if v := MemberVisibilityWith(maya, GateForRecord(auto), installedAt(0)); !v.Installed || reads != 0 {
		t.Fatalf("auto = %+v, reads %d", v, reads)
	}
	if v := MemberVisibilityWith(maya, GateForRecord(optin), installedAt(0)); !v.Visible || v.Installed || reads != 1 {
		t.Fatalf("optin not installed = %+v, reads %d", v, reads)
	}
	if v := MemberVisibilityWith(maya, GateForRecord(optin), installedAt(7)); !v.Installed || v.InstalledAtMs != 7 {
		t.Fatalf("optin installed = %+v", v)
	}
	reads = 0
	if v := MemberVisibilityWith(maya, GateForRecord(off), installedAt(7)); v.Visible || reads != 0 {
		t.Fatalf("off = %+v, reads %d", v, reads)
	}
	for _, c := range []struct {
		rec  Record
		want Distribution
	}{
		{auto, Distribution{Distributed: true}},
		{optin, Distribution{Distributed: true, OptIn: true}},
		{off, Distribution{}},
		{scoped, Distribution{}},
	} {
		if got := TenantDistribution(c.rec, "acme"); got != c.want {
			t.Errorf("TenantDistribution(%+v) = %+v, want %+v", c.rec, got, c.want)
		}
	}
}
