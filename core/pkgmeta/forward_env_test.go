package pkgmeta

import (
	"encoding/json"
	"os"
	"testing"
)

// The sandbox env forwarding rule against its fixture, which the desktop's
// copy (valid_forward_env, forward_env_pattern_matches_go) reads too.
func TestValidForwardEnvMatchesFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/forward_env_patterns.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Valid   []string `json:"valid"`
		Invalid []string `json:"invalid"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Valid) == 0 || len(fixture.Invalid) == 0 {
		t.Fatal("empty fixture")
	}
	for _, p := range fixture.Valid {
		if !ValidForwardEnv(p) {
			t.Errorf("ValidForwardEnv(%q) = false, want true", p)
		}
	}
	for _, p := range fixture.Invalid {
		if ValidForwardEnv(p) {
			t.Errorf("ValidForwardEnv(%q) = true, want false", p)
		}
	}
}
