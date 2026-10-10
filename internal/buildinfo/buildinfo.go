// Package buildinfo is the build identity of a bnbloc binary: the source
// commit and build time, stamped at link time by every build surface (the
// service Dockerfiles, the all-in-one image, the desktop bridge sidecar
// script):
//
//	go build -ldflags "-X github.com/luckylhb90/bnbloc/internal/buildinfo.Commit=$(git rev-parse --short=12 HEAD) \
//	  -X github.com/luckylhb90/bnbloc/internal/buildinfo.BuiltAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// A plain `go build` in a git checkout falls back to the toolchain's VCS
// stamp (vcs.revision). Nothing here is hardcoded: an unstamped binary built
// outside a checkout reports an empty commit, which the deployment view
// flags as unknown.
package buildinfo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Commit and BuiltAt are set with -ldflags -X at link time.
var (
	Commit  string
	BuiltAt string
)

// Get returns the running binary's identity as service.
func Get(service string) wire.ServiceVersion {
	commit := strings.TrimSpace(Commit)
	if commit == "" {
		commit = vcsRevision()
	}
	host, _ := os.Hostname()
	return wire.ServiceVersion{Service: service, Commit: commit, BuiltAt: strings.TrimSpace(BuiltAt), Instance: host}
}

func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

// Handler serves GET /version: Get(service) as JSON.
func Handler(service string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(Get(service))
	})
}

// SameCommit reports whether a and b name the same commit: both known and
// one a prefix of the other (short and full hashes of one commit match).
func SameCommit(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// Evaluate fills d.Consistent and d.Problems from the reported services,
// web bundle and unreachable services. Services are sorted by name then
// instance for stable output.
func Evaluate(d *wire.DeploymentVersions) {
	sort.Slice(d.Services, func(i, j int) bool {
		if d.Services[i].Service != d.Services[j].Service {
			return d.Services[i].Service < d.Services[j].Service
		}
		return d.Services[i].Instance < d.Services[j].Instance
	})
	var problems, warnings []string
	for _, u := range d.Unreachable {
		problems = append(problems, fmt.Sprintf("%s unreachable: %s", u.Service, u.Error))
	}
	type group struct {
		commit  string
		members []string
	}
	var groups []*group
	add := func(commit, who string) {
		if commit == "" {
			// Unverifiable, not skew: the image was built without the
			// commit stamp, so there is nothing to compare.
			warnings = append(warnings, who+": commit unknown (built without CI_COMMIT_SHA)")
			return
		}
		for _, g := range groups {
			if SameCommit(g.commit, commit) {
				if len(commit) > len(g.commit) {
					g.commit = commit
				}
				g.members = append(g.members, who)
				return
			}
		}
		groups = append(groups, &group{commit: commit, members: []string{who}})
	}
	for _, s := range d.Services {
		add(s.Commit, s.Service+"@"+s.Instance)
	}
	if d.Web == nil {
		problems = append(problems, "web bundle: build identity missing")
	} else {
		add(d.Web.Commit, "web bundle")
	}
	if len(groups) > 1 {
		parts := make([]string, len(groups))
		for i, g := range groups {
			parts[i] = fmt.Sprintf("%s (%s)", short(g.commit), strings.Join(g.members, ", "))
		}
		problems = append(problems, "commit skew: "+strings.Join(parts, " vs "))
	}
	d.Problems = problems
	d.Warnings = warnings
	d.Consistent = len(problems) == 0
}

func short(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
