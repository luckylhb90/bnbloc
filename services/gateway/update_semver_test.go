package gateway

import "testing"

func TestCompareSemver_whenPatchHasTwoDigits(t *testing.T) {
	got, err := compareSemver("0.1.10", "0.1.9")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if got <= 0 {
		t.Fatalf("compareSemver(0.1.10, 0.1.9) = %d, want positive", got)
	}
}

func TestCompareSemver_whenPrereleaseComparedToRelease(t *testing.T) {
	got, err := compareSemver("1.0.0-beta.2", "1.0.0")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if got >= 0 {
		t.Fatalf("compareSemver prerelease = %d, want negative", got)
	}
}

func TestParseSemver_whenInvalid(t *testing.T) {
	if _, err := parseSemver("0.1"); err == nil {
		t.Fatal("parseSemver accepted missing patch")
	}
}
