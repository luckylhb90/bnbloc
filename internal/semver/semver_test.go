package semver

import "testing"

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.1.10", "0.1.9", 1},
		{"1.0.0-beta.2", "1.0.0", -1},
		{"1.0.0-beta.2", "1.0.0-beta.10", -1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"v2.0.0", "2.0.0", 0},
		{"1.2.3+build", "1.2.3", 0},
	} {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Fatalf("Compare(%s, %s) = %d, %v; want %d", c.a, c.b, got, err, c.want)
		}
	}
	for _, bad := range []string{"0.1", "01.0.0", "1.0.0-", "a.b.c", ""} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) accepted", bad)
		}
	}
}

func TestSatisfies(t *testing.T) {
	for _, c := range []struct {
		v, rng string
		want   bool
	}{
		{"1.4.0", "^1", true},
		{"2.0.0", "^1", false},
		{"1.2.9", "^1.3", false},
		{"1.3.0", "^1.3", true},
		{"0.3.5", "^0.3.1", true},
		{"0.4.0", "^0.3.1", false},
		{"1.2.9", "~1.2", true},
		{"1.3.0", "~1.2", false},
		{"1.2.3", "1.2.3", true},
		{"1.2.4", "1.2.3", false},
		{"1.2.4", "1.2", true},
		{"3.0.0", ">=1.2.3", true},
		{"1.0.0", "", true},
		{"1.0.0-rc.1", "*", false},
		{"1.0.0-rc.1", "1.0.0-rc.1", true},
		{"1.0.0-rc.1", "^1", false},
	} {
		got, err := Satisfies(c.v, c.rng)
		if err != nil || got != c.want {
			t.Fatalf("Satisfies(%s, %q) = %v, %v; want %v", c.v, c.rng, got, err, c.want)
		}
	}
	for _, bad := range []string{"^x", "~1.x", ">=", "1.2.3.4"} {
		if ValidRange(bad) {
			t.Fatalf("ValidRange(%q)", bad)
		}
	}
	if v, err := BumpPatch("1.2.3-rc.1"); err != nil || v != "1.2.4" {
		t.Fatalf("BumpPatch = %s, %v", v, err)
	}
}

func TestValid(t *testing.T) {
	for v, ok := range map[string]bool{
		"1.0.0": true, "0.1.2-rc.1": true, "1.2.3+build.7": true,
		"v1.0.0": false, "1.0.0 ": false, "1.0": false, "01.0.0": false, "1.0.0+": false, "1.0.0+a..b": false,
		"1.0.0-١": false, "١.0.0": false,
	} {
		if Valid(v) != ok {
			t.Errorf("Valid(%q) != %v", v, ok)
		}
	}
}
