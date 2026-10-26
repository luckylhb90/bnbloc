package main

import (
	"strings"
	"testing"
)

// The two words the scenario looks for are distinct, share a suffix, and are not guessable
// from the prompt (a reply that merely echoes the instruction must not pass).
func TestSteerWordsAreDistinct(t *testing.T) {
	a, b, err := steerWords()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, "S1-") || !strings.HasPrefix(b, "S2-") || a == b || strings.TrimPrefix(a, "S1-") != strings.TrimPrefix(b, "S2-") {
		t.Fatalf("words %q %q", a, b)
	}
	c, _, _ := steerWords()
	if c == a {
		t.Fatalf("two runs got the same word %q", a)
	}
}

func TestSteerScenarioIsKnown(t *testing.T) {
	if scenarioSteer != "steer" {
		t.Fatalf("scenario name %q", scenarioSteer)
	}
}
