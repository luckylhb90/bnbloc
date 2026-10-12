package pkgmeta

import (
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

const testKey = "sha256/" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// The gateway artifact URL is built and parsed only here, so the builder,
// the entitlement parser and the publish-time validator agree.
func TestArtifactURLRoundTrip(t *testing.T) {
	url := GatewayArtifactURL("https://bnbloc.example.com/", testKey)
	if url != "https://bnbloc.example.com/v1/artifacts/"+testKey {
		t.Fatalf("url = %q", url)
	}
	for _, raw := range []string{url, GatewayArtifactPath(testKey), "https://other.example.com/v1/artifacts/" + testKey} {
		if key, ok := ArtifactKeyOf(raw); !ok || key != testKey {
			t.Errorf("ArtifactKeyOf(%q) = %q %v", raw, key, ok)
		}
	}
	for _, raw := range []string{
		"https://bnbloc.example.com/v1/artifacts/builtin/abc",
		"https://bnbloc.example.com/v1/artifacts/sha256/ABC",
		url + "?x=1",
		url + "#f",
		"https://bnbloc.example.com/v1/artifacts/sha256/%2e%2e",
		"https://bnbloc.example.com/elsewhere/" + testKey,
	} {
		if key, ok := ArtifactKeyOf(raw); ok {
			t.Errorf("ArtifactKeyOf(%q) = %q, want refused", raw, key)
		}
	}
	base, _ := ParseGatewayArtifactBaseURL("https://bnbloc.example.com")
	if key, err := GatewayArtifactKey(url, base); err != nil || key != testKey {
		t.Fatalf("GatewayArtifactKey = %q %v", key, err)
	}
	if _, err := GatewayArtifactKey("https://other.example.com/v1/artifacts/"+testKey, base); err == nil {
		t.Fatal("another origin's artifact URL accepted")
	}
}

// TestArtifactURLOneOwner: no Go source outside this package spells the
// artifact route; it builds and parses through GatewayArtifactURL /
// GatewayArtifactPath / ArtifactKeyOf (2026-10-12: three parsers, one
// loose, compared keys from different parsers for entitlement).
func TestArtifactURLOneOwner(t *testing.T) {
	var problems []string
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "cmd", "core"}}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "core/pkgmeta/") {
			return
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, `"/v1/artifacts/"`) || strings.Contains(line, `/v1/artifacts/" +`) {
				problems = append(problems, rel+":"+strconv.Itoa(i+1))
			}
		}
	})
	if len(problems) > 0 {
		t.Fatalf("the artifact route spelled outside pkgmeta (use GatewayArtifactURL/Path, ArtifactKeyOf):\n  %s", strings.Join(problems, "\n  "))
	}
}
