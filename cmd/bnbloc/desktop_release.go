package main

// Desktop release rules for `bnbloc updates publish`.
//
// These are the Go mirror of the web Platform → Updates publish wizard:
//   - web/src/lib/desktopRelease.ts   (validateDesktopReleaseSelection,
//     isDesktopReleaseVersion, prepareDesktopRelease, publishDesktopRelease)
//   - web/src/views/updateConfigFields.ts (updateCandidateForArchive)
//   - web/src/lib/artifactUpload.ts   (upload response check)
//
// The CLI cannot import TypeScript, so the rules live once per language and
// MUST be changed together; desktop_release_test.go mirrors the web tests
// (web/src/lib/updateConfig.test.ts) for the candidate config.

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// desktopArchiveName is the Tauri updater archive the publish flow accepts
// (the DMG installer is not an updater payload).
const desktopArchiveName = "bnbloc.app.tar.gz"

// tauriSignatureHeader starts every decoded Tauri (minisign) updater signature.
const tauriSignatureHeader = "untrusted comment: signature from tauri secret key\n"

var (
	desktopVersionRE   = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
	strictBase64RE     = regexp.MustCompile(`^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$`)
	desktopArchiveRE   = regexp.MustCompile(`(?i)^bnbloc\.app\.tar\.gz$`)
	errArchiveNotTauri = fmt.Errorf("select the signed %s updater archive, not a DMG installer", desktopArchiveName)
)

// desktopTarget describes where a published archive lands in the Release Hub.
type desktopTarget struct {
	Platform string
	Arch     string
}

// desktopPublishTargets are the targets whose updater payload is the signed
// bnbloc.app.tar.gz (macOS). The web wizard publishes darwin-aarch64 only;
// its hub file shape is the darwin-aarch64 row here.
var desktopPublishTargets = map[string]desktopTarget{
	"darwin-aarch64": {Platform: "macOS", Arch: "Apple Silicon"},
	"darwin-x86_64":  {Platform: "macOS", Arch: "Intel"},
}

// isDesktopReleaseVersion mirrors desktopRelease.ts isDesktopReleaseVersion.
func isDesktopReleaseVersion(version string) bool {
	return desktopVersionRE.MatchString(strings.TrimSpace(version))
}

// validateDesktopReleaseSelection mirrors desktopRelease.ts
// validateDesktopReleaseSelection: the archive is bnbloc.app.tar.gz and the
// signature is the base64 Tauri minisign signature generated for that exact
// file. It returns the trimmed signature.
func validateDesktopReleaseSelection(archiveName, signature string) (string, error) {
	if !desktopArchiveRE.MatchString(archiveName) {
		return "", errArchiveNotTauri
	}
	encoded := strings.TrimSpace(signature)
	if encoded == "" {
		return "", fmt.Errorf("the paired Tauri signature file is required")
	}
	if !strictBase64RE.MatchString(encoded) {
		return "", fmt.Errorf("the signature file is not valid base64 Tauri signature data")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("the signature file is not valid base64 Tauri signature data")
	}
	decoded := string(raw)
	if !strings.HasPrefix(decoded, tauriSignatureHeader) {
		return "", fmt.Errorf("the signature file is not a Tauri Minisign updater signature")
	}
	lines := strings.Split(strings.TrimRightFunc(decoded, unicode.IsSpace), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[2], "trusted comment: ") || lines[3] == "" {
		return "", fmt.Errorf("the signature file is incomplete")
	}
	if !strings.Contains(lines[2], "file:"+archiveName) {
		return "", fmt.Errorf("the updater signature was generated for a different archive file")
	}
	return encoded, nil
}

// candidateRelease is what one publish contributes to the updater config.
type candidateRelease struct {
	Version   string
	Notes     string
	PubDate   string
	Target    string
	URL       string
	Signature string
}

// updateCandidateForArchive mirrors updateConfigFields.ts
// updateCandidateForArchive: the operator's policy is preserved (channel,
// auto-check, interval, auto-install, minimum version); only Enabled turns
// on, because publishing is the act of rolling the release out. Every target
// shares the release's single version, so when the version changes the other
// targets' artifacts (built for the previous version) are cleared instead of
// being re-labelled as the new one; re-publishing the same version keeps them.
func updateCandidateForArchive(cfg wire.UpdateConfig, rel candidateRelease) wire.UpdateConfig {
	sameVersion := strings.TrimSpace(cfg.Release.Version) == strings.TrimSpace(rel.Version)
	platforms := make(map[string]wire.UpdatePlatform, len(wire.UpdateTargets))
	for _, target := range wire.UpdateTargets {
		platforms[target] = wire.UpdatePlatform{}
		if sameVersion {
			platforms[target] = cfg.Release.Platforms[target]
		}
	}
	platforms[rel.Target] = wire.UpdatePlatform{URL: rel.URL, Signature: rel.Signature}
	policy := cfg.Policy
	policy.Enabled = true
	return wire.UpdateConfig{
		Policy:  policy,
		Release: wire.UpdateRelease{Version: rel.Version, Notes: rel.Notes, PubDate: rel.PubDate, Platforms: platforms},
	}
}

// uploadedArchive is the gateway-stored archive plus the release details.
type uploadedArchive struct {
	Name      string
	URL       string
	SHA256    string
	Size      int64
	Signature string
	Version   string
	Notes     string
	PubDate   string
	Target    string
	// Channel is the update policy channel the operator published under (the
	// channel read before the upload).
	Channel string
}

// prepareDesktopRelease mirrors desktopRelease.ts prepareDesktopRelease: it
// validates the uploaded archive against the CURRENT config and hub, and
// returns the updater config + Release Hub entry to write.
func prepareDesktopRelease(cfg wire.UpdateConfig, hub []wire.ReleaseEntry, a uploadedArchive) (wire.UpdateConfig, wire.ReleaseEntry, error) {
	if !desktopArchiveRE.MatchString(strings.TrimSpace(a.Name)) {
		return wire.UpdateConfig{}, wire.ReleaseEntry{}, errArchiveNotTauri
	}
	if strings.TrimSpace(a.Signature) == "" {
		return wire.UpdateConfig{}, wire.ReleaseEntry{}, fmt.Errorf("the paired updater signature is required")
	}
	u, err := url.Parse(a.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return wire.UpdateConfig{}, wire.ReleaseEntry{}, fmt.Errorf("the artifact URL must be an absolute HTTP(S) gateway URL")
	}
	if key, ok := pkgmeta.ArtifactKeyOf(a.URL); !ok || key != digest.Key(a.SHA256) {
		return wire.UpdateConfig{}, wire.ReleaseEntry{}, fmt.Errorf("the artifact URL must be the content-addressed URL returned by this gateway")
	}
	if a.Size <= 0 {
		return wire.UpdateConfig{}, wire.ReleaseEntry{}, fmt.Errorf("uploaded artifact size is invalid")
	}
	if !isDesktopReleaseVersion(a.Version) {
		return wire.UpdateConfig{}, wire.ReleaseEntry{}, fmt.Errorf("release version must be valid semver")
	}
	if _, err := time.Parse(time.RFC3339, a.PubDate); err != nil {
		return wire.UpdateConfig{}, wire.ReleaseEntry{}, fmt.Errorf("publish date must be a valid RFC3339 date")
	}
	target, ok := desktopPublishTargets[a.Target]
	if !ok {
		return wire.UpdateConfig{}, wire.ReleaseEntry{}, fmt.Errorf("target %q is not published from a %s archive", a.Target, desktopArchiveName)
	}
	if cfg.Policy.Channel != a.Channel {
		return wire.UpdateConfig{}, wire.ReleaseEntry{}, fmt.Errorf("the update policy now uses the %s channel, not %s; review the release again before publishing", cfg.Policy.Channel, a.Channel)
	}
	version := strings.TrimSpace(a.Version)
	signature := strings.TrimSpace(a.Signature)
	candidate := updateCandidateForArchive(cfg, candidateRelease{Version: version, Notes: a.Notes, PubDate: a.PubDate, Target: a.Target, URL: a.URL, Signature: signature})
	var previous *wire.ReleaseEntry
	for i := range hub {
		if hub[i].Version != version {
			continue
		}
		if hub[i].Channel != a.Channel {
			return wire.UpdateConfig{}, wire.ReleaseEntry{}, fmt.Errorf("that version already exists on another channel; refusing to replace it as %s", a.Channel)
		}
		previous = &hub[i]
	}
	files := []wire.ReleaseFile{}
	if previous != nil {
		for _, f := range previous.Files {
			if f.Target != a.Target {
				files = append(files, f)
			}
		}
	}
	files = append(files, wire.ReleaseFile{Platform: target.Platform, Arch: target.Arch, Target: a.Target, Name: a.Name, URL: a.URL, SHA256: a.SHA256, Size: a.Size})
	entry := wire.ReleaseEntry{Version: version, Channel: a.Channel, ReleaseDate: a.PubDate, Notes: a.Notes, Files: files}
	if previous != nil {
		entry.CreatedAtMs = previous.CreatedAtMs
		entry.CreatedBy = previous.CreatedBy
	}
	return candidate, entry, nil
}

// checkUploadResponse mirrors artifactUpload.ts: the key is content-addressed,
// the sha256 is the key's digest, and the size is the local file's. The CLI
// additionally pins the digest to the one it computed over the local file.
func checkUploadResponse(key, sha string, size int64, localSHA string, localSize int64) error {
	if keySHA, ok := digest.ParseKey(key); !ok || keySHA != sha || size != localSize {
		return fmt.Errorf("artifact upload response did not match the selected file (key %q, sha256 %q, size %d; local size %d)", key, sha, size, localSize)
	}
	if sha != localSHA {
		return fmt.Errorf("artifact upload sha256 %s does not match the local file's %s", sha, localSHA)
	}
	return nil
}
