package coreset

import (
	"encoding/json"
	"os"
	"testing"
)

// TestEnvKeyMatchesSharedFixture pins EnvKey to the fixture the desktop
// shell's env_key is tested against too (core_env_key_matches_go), so the
// BNBLOC_CORE_<NAME>_URL/_TOKEN the desktop writes are the ones FromEnv reads.
func TestEnvKeyMatchesSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/core_env_keys.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture struct {
		Cases []struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fixture.Cases {
		if got := EnvKey(c.Name); got != c.Key {
			t.Errorf("EnvKey(%q) = %q, want %q", c.Name, got, c.Key)
		}
	}
}
