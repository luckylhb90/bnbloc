package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func runUpdates(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("updates: expected a subcommand (publish|status)")
	}
	switch args[0] {
	case "publish":
		return updatesPublish(args[1:])
	case "status":
		return updatesStatus(args[1:])
	default:
		return fmt.Errorf("updates: unknown subcommand %q", args[0])
	}
}

// desktopPublishInput is one `updates publish` request.
type desktopPublishInput struct {
	ArchivePath string
	Signature   string // raw .sig file content
	Version     string
	Notes       string
	Target      string
	Now         func() time.Time
}

func updatesPublish(args []string) error {
	fs := flag.NewFlagSet("updates publish", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "gateway base URL")
	token := fs.String("token", "", "super-admin session token")
	archive := fs.String("archive", "", "signed updater archive (bnbloc.app.tar.gz)")
	signature := fs.String("signature", "", "Tauri signature file generated for the archive (.sig)")
	version := fs.String("version", "", "release version (semver, e.g. 1.2.3)")
	notes := fs.String("notes", "", "release notes (markdown)")
	notesFile := fs.String("notes-file", "", "read release notes from a file")
	target := fs.String("target", "darwin-aarch64", "updater target (darwin-aarch64 | darwin-x86_64)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *archive == "" || *signature == "" || *version == "" {
		return fmt.Errorf("updates publish: --archive, --signature, and --version are required")
	}
	if *notes != "" && *notesFile != "" {
		return fmt.Errorf("updates publish: use --notes or --notes-file, not both")
	}
	releaseNotes := *notes
	if *notesFile != "" {
		raw, err := os.ReadFile(expandHome(*notesFile))
		if err != nil {
			return err
		}
		releaseNotes = string(raw)
	}
	sig, err := os.ReadFile(expandHome(*signature))
	if err != nil {
		return err
	}
	c := newClient(resolveConfig(*gateway, *token))
	_, err = publishDesktopUpdate(c, desktopPublishInput{
		ArchivePath: expandHome(*archive), Signature: string(sig), Version: *version, Notes: releaseNotes, Target: *target, Now: time.Now,
	}, os.Stdout)
	return err
}

// publishDesktopUpdate mirrors the web wizard's publishDesktopRelease: upload
// the archive, then (against freshly loaded state) write the Release Hub
// entry, then activate the updater under the CURRENT policy channel. The
// updater activates only after the hub entry saves; each stage is reported,
// and a failure says which stages already completed.
func publishDesktopUpdate(c *client, in desktopPublishInput, out io.Writer) (wire.UpdateConfig, error) {
	step := func(n int, format string, a ...any) {
		fmt.Fprintf(out, "[%d/6] %s\n", n, fmt.Sprintf(format, a...))
	}
	name := filepath.Base(in.ArchivePath)
	// Stage 1: local checks (same rules as the wizard's "Signed files" and
	// "Release info" steps).
	if name != desktopArchiveName {
		return wire.UpdateConfig{}, errArchiveNotTauri
	}
	sig, err := validateDesktopReleaseSelection(name, in.Signature)
	if err != nil {
		return wire.UpdateConfig{}, err
	}
	version := strings.TrimSpace(in.Version)
	if !isDesktopReleaseVersion(version) {
		return wire.UpdateConfig{}, fmt.Errorf("enter a valid release version (for example, 1.2.3)")
	}
	if _, ok := desktopPublishTargets[in.Target]; !ok {
		return wire.UpdateConfig{}, fmt.Errorf("--target %q is not published from a %s archive (want darwin-aarch64 or darwin-x86_64)", in.Target, desktopArchiveName)
	}
	localSHA, localSize, err := fileDigest(in.ArchivePath)
	if err != nil {
		return wire.UpdateConfig{}, err
	}
	step(1, "checked %s (%d bytes, sha256 %s) and its Tauri signature", name, localSize, localSHA)

	// Stage 2: the channel the release is published under is the saved
	// policy's (the wizard's review step shows it before anything is written).
	reviewed, err := getUpdateConfig(c)
	if err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("load update config: %w", err)
	}
	channel := reviewed.Policy.Channel
	step(2, "current updater: version %s on the %s channel (enabled %v); publishing %s for %s", orNone(reviewed.Release.Version), channel, reviewed.Policy.Enabled, version, in.Target)

	// Stage 3: upload.
	uploaded, err := uploadArtifactFile(c, in.ArchivePath)
	if err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("upload failed; nothing was published: %w", err)
	}
	step(3, "uploaded %s -> %s", name, uploaded.Key)

	// Stage 4: read it back the way clients will.
	if err := verifyArtifactDownload(c, uploaded); err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("artifact uploaded (%s) but download verification failed; nothing was published: %w", uploaded.Key, err)
	}
	step(4, "verified download of %s (sha256 + size)", uploaded.Key)

	// Stage 5: Release Hub, against the latest state.
	current, err := getUpdateConfig(c)
	if err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("artifact uploaded (%s), but loading the update config failed; nothing was published: %w", uploaded.Key, err)
	}
	hub, err := getReleaseHub(c)
	if err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("artifact uploaded (%s), but loading the Release Hub failed; nothing was published: %w", uploaded.Key, err)
	}
	for _, rel := range hub.Releases {
		if rel.Version == version && rel.Channel != channel {
			return wire.UpdateConfig{}, fmt.Errorf("artifact uploaded (%s), but that version now exists on another channel; refusing to publish over the latest state", uploaded.Key)
		}
	}
	candidate, entry, err := prepareDesktopRelease(current, hub.Releases, uploadedArchive{
		Name: name, URL: uploaded.URL, SHA256: uploaded.SHA256, Size: uploaded.Size, Signature: sig,
		Version: version, Notes: in.Notes, PubDate: in.Now().UTC().Format(time.RFC3339), Target: in.Target, Channel: channel,
	})
	if err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("artifact uploaded (%s), but the release was not published: %w", uploaded.Key, err)
	}
	hubBody, _ := json.Marshal(struct {
		Channel     string             `json:"channel"`
		ReleaseDate string             `json:"releaseDate"`
		Notes       string             `json:"notes"`
		Files       []wire.ReleaseFile `json:"files"`
	}{entry.Channel, entry.ReleaseDate, entry.Notes, entry.Files})
	if _, err := c.do(http.MethodPut, "/v1/platform/app-releases/"+url.PathEscape(entry.Version), hubBody); err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("artifact uploaded (%s), but Release Hub save failed. Auto-update is not activated. %w", uploaded.Key, err)
	}
	step(5, "saved Release Hub entry %s (%s channel, %d file(s))", entry.Version, entry.Channel, len(entry.Files))

	// Stage 6: activate the updater.
	cfgBody, _ := json.Marshal(candidate)
	raw, err := c.do(http.MethodPut, "/v1/platform/update-config", cfgBody)
	if err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("Release Hub saved, but updater configuration failed. Update rollout is not activated. %w", err)
	}
	var saved wire.UpdateConfig
	if err := json.Unmarshal(raw, &saved); err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("Release Hub saved and updater configured, but the response was unreadable: %w", err)
	}
	step(6, "activated the %s updater: v%s for %s. Existing clients discover it on their next check.", saved.Policy.Channel, saved.Release.Version, in.Target)
	for _, t := range wire.UpdateTargets {
		before, after := reviewed.Release.Platforms[t], saved.Release.Platforms[t]
		if t != in.Target && before.URL != "" && after.URL == "" {
			fmt.Fprintf(out, "      note: %s artifact removed (it was built for v%s)\n", t, reviewed.Release.Version)
		}
	}
	return saved, nil
}

func getUpdateConfig(c *client) (wire.UpdateConfig, error) {
	raw, err := c.do(http.MethodGet, "/v1/platform/update-config", nil)
	if err != nil {
		return wire.UpdateConfig{}, err
	}
	var cfg wire.UpdateConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return wire.UpdateConfig{}, fmt.Errorf("update config response invalid: %w", err)
	}
	return cfg, nil
}

func getReleaseHub(c *client) (wire.ReleaseHub, error) {
	raw, err := c.do(http.MethodGet, "/v1/platform/app-releases", nil)
	if err != nil {
		return wire.ReleaseHub{}, err
	}
	var hub wire.ReleaseHub
	if err := json.Unmarshal(raw, &hub); err != nil {
		return wire.ReleaseHub{}, fmt.Errorf("release hub response invalid: %w", err)
	}
	return hub, nil
}

func updatesStatus(args []string) error {
	fs := flag.NewFlagSet("updates status", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "gateway base URL")
	token := fs.String("token", "", "super-admin session token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := newClient(resolveConfig(*gateway, *token))
	cfg, err := getUpdateConfig(c)
	if err != nil {
		return err
	}
	hub, err := getReleaseHub(c)
	if err != nil {
		return err
	}
	printUpdateStatus(os.Stdout, cfg, hub)
	return nil
}

func printUpdateStatus(out io.Writer, cfg wire.UpdateConfig, hub wire.ReleaseHub) {
	p := cfg.Policy
	fmt.Fprintf(out, "policy   enabled=%v channel=%s autoCheck=%v interval=%dh autoInstall=%v minSupported=%s\n",
		p.Enabled, p.Channel, p.AutoCheck, p.CheckIntervalHours, p.AutoInstall, orNone(p.MinSupportedVersion))
	fmt.Fprintf(out, "release  version=%s pubDate=%s\n", orNone(cfg.Release.Version), orNone(cfg.Release.PubDate))
	for _, t := range wire.UpdateTargets {
		fmt.Fprintf(out, "  %-15s %s\n", t, orNone(cfg.Release.Platforms[t].URL))
	}
	fmt.Fprintln(out, "release hub (newest first):")
	if len(hub.Releases) == 0 {
		fmt.Fprintln(out, "  (empty)")
	}
	for i, rel := range hub.Releases {
		if i == 5 {
			fmt.Fprintf(out, "  … %d more\n", len(hub.Releases)-i)
			break
		}
		fmt.Fprintf(out, "  %-12s %-8s %s  %d file(s)\n", rel.Version, rel.Channel, rel.ReleaseDate, len(rel.Files))
	}
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}
