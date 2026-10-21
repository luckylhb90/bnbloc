package protocol

import (
	"encoding/json"
	"os"
	"testing"
)

// The private-host rule against its fixture, which the desktop's copy
// (valid_private_host, private_host_rule_matches_go) reads too.
func TestValidPrivateHostMatchesFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/private_hosts.json")
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
	for _, h := range fixture.Valid {
		if !ValidPrivateHost(h) {
			t.Errorf("ValidPrivateHost(%q) = false, want true", h)
		}
	}
	for _, h := range fixture.Invalid {
		if ValidPrivateHost(h) {
			t.Errorf("ValidPrivateHost(%q) = true, want false", h)
		}
	}
}
