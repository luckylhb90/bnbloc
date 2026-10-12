package wire

// Desktop update + release hub DTOs. The gateway serves and stores them
// (GET/PUT /v1/platform/update-config, /v1/platform/app-releases) and the
// operator CLI (`bnbloc updates publish`) writes them, so both use these one
// shapes, and the web reads them from wire.gen.ts (generated).

// Update channels a desktop build can follow.
const (
	UpdateChannelStable   = "stable"
	UpdateChannelInternal = "internal"
)

// UpdateTargets are the Tauri updater targets the gateway serves. They are
// the updater's own platform identifiers (a protocol constant of the Tauri
// updater, not deployment data).
var UpdateTargets = []string{"darwin-aarch64", "darwin-x86_64", "windows-x86_64"}

// IsUpdateTarget reports whether target is one of UpdateTargets.
func IsUpdateTarget(target string) bool {
	for _, t := range UpdateTargets {
		if t == target {
			return true
		}
	}
	return false
}

// UpdatePolicy is the operator-controlled desktop update policy.
type UpdatePolicy struct {
	Enabled             bool   `json:"enabled"`
	Channel             string `json:"channel"`
	AutoCheck           bool   `json:"autoCheck"`
	CheckIntervalHours  int    `json:"checkIntervalHours"`
	AutoInstall         bool   `json:"autoInstall"`
	MinSupportedVersion string `json:"minSupportedVersion"`
}

// UpdatePlatform is one target's signed updater artifact.
type UpdatePlatform struct {
	URL       string `json:"url"`
	Signature string `json:"signature"`
}

// UpdateRelease is the release the updater currently offers. Every target
// shares its single version.
type UpdateRelease struct {
	Version   string                    `json:"version"`
	Notes     string                    `json:"notes"`
	PubDate   string                    `json:"pubDate"`
	Platforms map[string]UpdatePlatform `json:"platforms"`
}

// UpdateConfig is the full super-admin update configuration.
type UpdateConfig struct {
	Policy  UpdatePolicy  `json:"policy"`
	Release UpdateRelease `json:"release"`
}

// ReleaseFile is one downloadable artifact for a version/platform.
type ReleaseFile struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Target   string `json:"target,omitempty"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// ReleaseEntry is one published version of bnbloc. Notes is markdown so the
// changelog can grow structure without a schema change.
type ReleaseEntry struct {
	Version     string        `json:"version"`
	Channel     string        `json:"channel"`
	ReleaseDate string        `json:"releaseDate"`
	Notes       string        `json:"notes"`
	Files       []ReleaseFile `json:"files"`
	CreatedAtMs int64         `json:"createdAtMs,omitempty"`
	CreatedBy   string        `json:"createdBy,omitempty"`
}

// ReleaseHubMeta is the instance-wide project intro shown above the history.
type ReleaseHubMeta struct {
	Name    string `json:"name"`
	Tagline string `json:"tagline"`
	Intro   string `json:"intro"`
}

// ReleaseHub is the release hub payload: intro + history, newest first.
type ReleaseHub struct {
	Project  ReleaseHubMeta `json:"project"`
	Releases []ReleaseEntry `json:"releases"`
}
