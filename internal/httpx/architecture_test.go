package httpx

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// TestOneErrorBodyOwner: every bnbloc HTTP surface (the gateway and every
// backend) answers errors with one body, written by httpx.Error /
// WriteStatus / Unavailable, and decodes a JSON request through DecodeJSON
// (413 on an oversized body, 400 on malformed JSON, everywhere). A
// plain-text http.Error, a private writeJSON/writeErr pair or a hand-rolled
// request decoder is a second owner whose shape and status codes drift
// (2026-10-12: the gateway answered text/plain beside the backends'
// {"error"}; oversized bodies got 400 in some services, 413 in others).
func TestOneErrorBodyOwner(t *testing.T) {
	type rule struct {
		re    *regexp.Regexp
		what  string
		allow map[string]string // path prefix -> reason
	}
	below := "below internal/httpx (it imports obs): health, readiness and version probes"
	rules := []rule{
		{regexp.MustCompile(`\bhttp\.Error\(`), "http.Error (use httpx.Error)", map[string]string{
			"internal/obs/":              below,
			"internal/buildinfo/":        below,
			"services/bridge/roomtools/": "the loopback MCP server speaks JSON-RPC, not the bnbloc API",
		}},
		{regexp.MustCompile(`(?m)^func (writeJSON|writeErr|writeError)\(`), "a private JSON/error writer (use httpx.WriteJSON / httpx.Error)", map[string]string{
			"services/bridge/roomtools/":  "the loopback MCP server speaks JSON-RPC, not the bnbloc API",
			"internal/search/searchtest/": "a fake OpenSearch server for tests",
		}},
		{regexp.MustCompile(`json\.NewDecoder\((r\.Body|http\.MaxBytesReader\(w, r\.Body)`), "a hand-rolled request decoder (use httpx.DecodeJSON)", map[string]string{
			"internal/httpx/":                  "the owner",
			"services/realtime/dispatch.go":    "a malformed dispatch is refused with the dispatch code (wire.DispatchInvalid) the room tools render",
			"services/gateway/mcp_packages.go": "the console's MCP form refuses unknown fields (DisallowUnknownFields) and names them",
		}},
	}
	var problems []string
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal"}}, func(rel string, src []byte) {
		for _, r := range rules {
			if !r.re.Match(src) || allowed(rel, r.allow) {
				continue
			}
			problems = append(problems, rel+": "+r.what)
		}
	})
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("a second error-body or decode owner:\n  %s", strings.Join(problems, "\n  "))
	}
}

func allowed(rel string, allow map[string]string) bool {
	for prefix := range allow {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}
