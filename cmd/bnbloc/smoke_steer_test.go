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

// A job that ended without replying says why (the reason and the core's error), so a failed
// smoke names the cause instead of timing out; one that may still run says nothing.
func TestSteerJobSaysHowItFailed(t *testing.T) {
	for _, c := range []struct {
		job  steerJob
		want string
	}{
		{steerJob{Status: "running"}, ""},
		{steerJob{Status: "replied"}, ""},
		{steerJob{Status: "failed", Reason: "agent_error", Error: "403 model not available"}, "failed agent_error: 403 model not available"},
		{steerJob{Status: "timed_out"}, "timed_out:"},
	} {
		if got := c.job.failedHow(); got != c.want {
			t.Errorf("%+v: failedHow = %q, want %q", c.job, got, c.want)
		}
	}
}
