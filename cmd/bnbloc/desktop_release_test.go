package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// testUpdateConfig mirrors config() in web/src/lib/updateConfig.test.ts.
func testUpdateConfig() wire.UpdateConfig {
	return wire.UpdateConfig{
		Policy: wire.UpdatePolicy{Enabled: false, Channel: "internal", AutoCheck: false, CheckIntervalHours: 12, AutoInstall: false},
		Release: wire.UpdateRelease{
			Version: "0.1.2",
			Platforms: map[string]wire.UpdatePlatform{
				"darwin-aarch64": {URL: "https://gw/v1/artifacts/sha256/old", Signature: "old"},
				"darwin-x86_64":  {},
				"windows-x86_64": {URL: "https://win/app.zip", Signature: "win"},
			},
		},
	}
}

// Mirrors testDeltaShowsPolicyAndEveryPlatformEffect: only `enabled` flips
// (channel stays internal, autoCheck stays off), the published target is
// replaced, and an artifact built for the old version is removed.
func TestCandidateFlipsOnlyEnabledAndClearsStaleTargets(t *testing.T) {
	current := testUpdateConfig()
	got := updateCandidateForArchive(current, candidateRelease{Version: "0.1.3", Notes: "n", PubDate: "2026-10-07T00:00:00Z", Target: "darwin-aarch64", URL: "https://gw/v1/artifacts/sha256/new", Signature: "new"})
	want := current.Policy
	want.Enabled = true
	if got.Policy != want {
		t.Fatalf("policy = %+v, want only enabled flipped from %+v", got.Policy, current.Policy)
	}
	if got.Release.Version != "0.1.3" || got.Release.Notes != "n" || got.Release.PubDate != "2026-10-07T00:00:00Z" {
		t.Fatalf("release = %+v", got.Release)
	}
	if p := got.Release.Platforms["darwin-aarch64"]; p.URL != "https://gw/v1/artifacts/sha256/new" || p.Signature != "new" {
		t.Fatalf("published target = %+v", p)
	}
	if p := got.Release.Platforms["windows-x86_64"]; p.URL != "" || p.Signature != "" {
		t.Fatalf("stale windows artifact kept under the new version: %+v", p)
	}
	if len(got.Release.Platforms) != len(wire.UpdateTargets) {
		t.Fatalf("platforms = %v, want every target present", got.Release.Platforms)
	}
	if current.Release.Platforms["windows-x86_64"].URL == "" {
		t.Fatal("candidate mutated the input config")
	}
}

// Mirrors testSameVersionKeepsOtherArtifacts.
func TestCandidateSameVersionKeepsOtherArtifacts(t *testing.T) {
	got := updateCandidateForArchive(testUpdateConfig(), candidateRelease{Version: "0.1.2", Target: "darwin-aarch64", URL: "https://gw/v1/artifacts/sha256/new", Signature: "new"})
	if p := got.Release.Platforms["windows-x86_64"]; p.URL != "https://win/app.zip" {
		t.Fatalf("same-version re-publish dropped the windows artifact: %+v", p)
	}
}

// tauriSig builds a .sig file body the way the Tauri signer does.
func tauriSig(file string) string {
	decoded := tauriSignatureHeader + "RUQfakekeyline\ntrusted comment: timestamp:1759900000\tfile:" + file + "\nZmFrZXNpZw==\n"
	return base64.StdEncoding.EncodeToString([]byte(decoded)) + "\n"
}

func TestValidateDesktopReleaseSelection(t *testing.T) {
	if sig, err := validateDesktopReleaseSelection("bnbloc.app.tar.gz", tauriSig("bnbloc.app.tar.gz")); err != nil || strings.HasSuffix(sig, "\n") {
		t.Fatalf("valid selection: %q %v", sig, err)
	}
	cases := map[string][2]string{
		"dmg":           {"bnbloc.dmg", tauriSig("bnbloc.dmg")},
		"empty sig":     {"bnbloc.app.tar.gz", "  \n"},
		"not base64":    {"bnbloc.app.tar.gz", "not base64!"},
		"not tauri":     {"bnbloc.app.tar.gz", base64.StdEncoding.EncodeToString([]byte("untrusted comment: other\na\nb\nc\n"))},
		"incomplete":    {"bnbloc.app.tar.gz", base64.StdEncoding.EncodeToString([]byte(tauriSignatureHeader + "RUQ\n"))},
		"other archive": {"bnbloc.app.tar.gz", tauriSig("other.app.tar.gz")},
	}
	for name, c := range cases {
		if _, err := validateDesktopReleaseSelection(c[0], c[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestIsDesktopReleaseVersion(t *testing.T) {
	for _, v := range []string{"1.2.3", " 0.1.10 ", "1.2.3-rc.1"} {
		if !isDesktopReleaseVersion(v) {
			t.Errorf("%q rejected", v)
		}
	}
	for _, v := range []string{"", "1.2", "v1.2.3", "1.2.3+build", "1.2.3-"} {
		if isDesktopReleaseVersion(v) {
			t.Errorf("%q accepted", v)
		}
	}
}

const testSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testArchive() uploadedArchive {
	return uploadedArchive{
		Name: "bnbloc.app.tar.gz", URL: "https://gw.example.com/v1/artifacts/sha256/" + testSHA, SHA256: testSHA, Size: 42,
		Signature: "sig", Version: "0.1.3", Notes: "notes", PubDate: "2026-10-08T00:00:00Z", Target: "darwin-aarch64", Channel: "internal",
	}
}

// The hub entry keeps the previous version's other files and replaces this
// target's (including a legacy file without a target); creation metadata of
// the previous row is preserved.
func TestPrepareDesktopReleaseMergesHubFiles(t *testing.T) {
	hub := []wire.ReleaseEntry{{
		Version: "0.1.3", Channel: "internal", CreatedAtMs: 7, CreatedBy: "ops",
		Files: []wire.ReleaseFile{
			{Platform: "macOS", Arch: "Apple Silicon", Name: "old", URL: "https://gw/legacy"},
			{Platform: "Windows", Arch: "x64", Target: "windows-x86_64", Name: "win", URL: "https://gw/win"},
		},
	}}
	cfg, entry, err := prepareDesktopRelease(testUpdateConfig(), hub, testArchive())
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Files) != 2 || entry.Files[0].Target != "windows-x86_64" || entry.Files[1].Target != "darwin-aarch64" || entry.Files[1].SHA256 != testSHA || entry.Files[1].Size != 42 {
		t.Fatalf("files = %+v", entry.Files)
	}
	if entry.CreatedAtMs != 7 || entry.CreatedBy != "ops" || entry.Channel != "internal" {
		t.Fatalf("entry = %+v", entry)
	}
	if cfg.Policy.Channel != "internal" || !cfg.Policy.Enabled || cfg.Release.Platforms["darwin-aarch64"].Signature != "sig" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestPrepareDesktopReleaseRefusals(t *testing.T) {
	mutate := map[string]func(*wire.UpdateConfig, *[]wire.ReleaseEntry, *uploadedArchive){
		"channel changed": func(c *wire.UpdateConfig, _ *[]wire.ReleaseEntry, _ *uploadedArchive) { c.Policy.Channel = "stable" },
		"version on other channel": func(_ *wire.UpdateConfig, h *[]wire.ReleaseEntry, _ *uploadedArchive) {
			*h = []wire.ReleaseEntry{{Version: "0.1.3", Channel: "stable"}}
		},
		"url not content-addressed": func(_ *wire.UpdateConfig, _ *[]wire.ReleaseEntry, a *uploadedArchive) {
			a.URL = "https://gw.example.com/v1/artifacts/sha256/other"
		},
		"url not http": func(_ *wire.UpdateConfig, _ *[]wire.ReleaseEntry, a *uploadedArchive) {
			a.URL = "file:///tmp/" + testSHA
		},
		"zero size":      func(_ *wire.UpdateConfig, _ *[]wire.ReleaseEntry, a *uploadedArchive) { a.Size = 0 },
		"bad version":    func(_ *wire.UpdateConfig, _ *[]wire.ReleaseEntry, a *uploadedArchive) { a.Version = "1.2" },
		"bad pub date":   func(_ *wire.UpdateConfig, _ *[]wire.ReleaseEntry, a *uploadedArchive) { a.PubDate = "yesterday" },
		"windows target": func(_ *wire.UpdateConfig, _ *[]wire.ReleaseEntry, a *uploadedArchive) { a.Target = "windows-x86_64" },
		"dmg":            func(_ *wire.UpdateConfig, _ *[]wire.ReleaseEntry, a *uploadedArchive) { a.Name = "bnbloc.dmg" },
	}
	for name, m := range mutate {
		cfg, hub, a := testUpdateConfig(), []wire.ReleaseEntry{}, testArchive()
		m(&cfg, &hub, &a)
		if _, _, err := prepareDesktopRelease(cfg, hub, a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckUploadResponse(t *testing.T) {
	if err := checkUploadResponse("sha256/"+testSHA, testSHA, 42, testSHA, 42); err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("f", 64)
	for name, c := range map[string]struct {
		key, sha string
		size     int64
	}{
		"key not content-addressed": {"blob/" + testSHA, testSHA, 42},
		"sha differs from key":      {"sha256/" + testSHA, other, 42},
		"size differs":              {"sha256/" + testSHA, testSHA, 41},
		"digest differs from local": {"sha256/" + other, other, 42},
	} {
		if err := checkUploadResponse(c.key, c.sha, c.size, testSHA, 42); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
