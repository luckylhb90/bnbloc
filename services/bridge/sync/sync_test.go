package sync

import "testing"

func TestVectorClockCompare(t *testing.T) {
	cases := []struct {
		name string
		a, b VectorClock
		want Ordering
	}{
		{"equal empty", VectorClock{}, VectorClock{}, Equal},
		{"equal same", VectorClock{"x": 2}, VectorClock{"x": 2}, Equal},
		{"a newer", VectorClock{"x": 3}, VectorClock{"x": 2}, Dominates},
		{"b newer", VectorClock{"x": 1}, VectorClock{"x": 2}, Dominated},
		{"a extends", VectorClock{"x": 2, "y": 1}, VectorClock{"x": 2}, Dominates},
		{"concurrent", VectorClock{"x": 2, "y": 0}, VectorClock{"x": 1, "y": 1}, Concurrent},
	}
	for _, c := range cases {
		if got := c.a.Compare(c.b); got != c.want {
			t.Errorf("%s: Compare = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestVectorClockMergeAndBump(t *testing.T) {
	a := VectorClock{"x": 2, "y": 1}
	b := VectorClock{"x": 1, "y": 3, "z": 5}
	m := a.Merge(b)
	if m["x"] != 2 || m["y"] != 3 || m["z"] != 5 {
		t.Fatalf("merge wrong: %v", m)
	}
	// Merge must not mutate inputs.
	if a["z"] != 0 || b["x"] != 1 {
		t.Fatalf("merge mutated an input: a=%v b=%v", a, b)
	}
	a.Bump("x")
	if a["x"] != 3 {
		t.Fatalf("bump wrong: %v", a)
	}
}

func TestThreeWayMergeCleanNonOverlapping(t *testing.T) {
	ancestor := []byte("line1\nline2\nline3")
	local := []byte("line1-local\nline2\nline3")   // changed first line
	remote := []byte("line1\nline2\nline3-remote") // changed last line
	merged, ok := threeWayMerge(ancestor, local, remote)
	if !ok {
		t.Fatalf("expected clean merge, got conflict: %s", merged)
	}
	want := "line1-local\nline2\nline3-remote"
	if string(merged) != want {
		t.Fatalf("merged = %q, want %q", merged, want)
	}
}

func TestThreeWayMergeIdenticalChange(t *testing.T) {
	ancestor := []byte("a\nb\nc")
	local := []byte("a\nB\nc")
	remote := []byte("a\nB\nc")
	merged, ok := threeWayMerge(ancestor, local, remote)
	if !ok || string(merged) != "a\nB\nc" {
		t.Fatalf("identical change should merge cleanly, got ok=%v %q", ok, merged)
	}
}

func TestThreeWayMergeConflict(t *testing.T) {
	ancestor := []byte("a\nb\nc")
	local := []byte("a\nLOCAL\nc")
	remote := []byte("a\nREMOTE\nc")
	merged, ok := threeWayMerge(ancestor, local, remote)
	if ok {
		t.Fatalf("expected conflict for overlapping edits, got clean: %q", merged)
	}
	// Conflict result must preserve both sides (no silent loss).
	s := string(merged)
	if !contains(s, "LOCAL") || !contains(s, "REMOTE") {
		t.Fatalf("conflict result dropped a side: %q", s)
	}
}

func TestThreeWayMergeBinaryRejected(t *testing.T) {
	ancestor := []byte{0, 1, 2}
	if _, ok := threeWayMerge(ancestor, []byte{0, 9}, []byte{0, 8}); ok {
		t.Fatal("binary content must not be line-merged")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
