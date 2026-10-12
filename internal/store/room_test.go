package store

import "testing"

func TestNormalizeSyncModeDefaultsGit(t *testing.T) {
	cases := map[string]SyncMode{
		"":        SyncModeGit,
		"bogus":   SyncModeGit,
		"git":     SyncModeGit,
		"mutagen": SyncModeMutagen,
		"none":    SyncModeNone,
	}
	for in, want := range cases {
		if got := NormalizeSyncMode(in); got != want {
			t.Fatalf("NormalizeSyncMode(%q) = %q, want %q", in, got, want)
		}
	}
}
