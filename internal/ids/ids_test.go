package ids

import (
	"strings"
	"testing"
	"time"
)

func TestIDsSortByCreationAndStayUnique(t *testing.T) {
	t0 := time.UnixMilli(1_700_000_000_000)
	a, b := at("art_", t0), at("art_", t0.Add(time.Millisecond))
	if !strings.HasPrefix(a, "art_") || len(a) != len("art_")+32 {
		t.Fatalf("shape %q", a)
	}
	if a >= b {
		t.Fatalf("a later id must sort after: %s >= %s", a, b)
	}
	if a[4:16] != "018bcfe56800" {
		t.Fatalf("time part %s", a[4:16])
	}
	seen := map[string]bool{}
	for range 10000 {
		id := New("x_")
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
	}
}
