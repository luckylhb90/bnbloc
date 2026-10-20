package pkgmeta

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/digest"
)

// gatewayArtifactPrefix is the gateway route a content-addressed artifact
// key is served under.
const gatewayArtifactPrefix = "/v1/artifacts/"

// The gateway artifact URL has one owner, this file: GatewayArtifactPath and
// GatewayArtifactURL build it, ArtifactKeyOf and GatewayArtifactKey parse it.

// GatewayArtifactPath is the gateway's download path of a blob key.
func GatewayArtifactPath(key string) string { return gatewayArtifactPrefix + key }

// GatewayArtifactURL is the absolute download URL of a blob key under the
// gateway's public origin base.
func GatewayArtifactURL(base, key string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + GatewayArtifactPath(key)
}

// ArtifactKeyOf is the content-addressed blob key ("sha256/<hex>") a
// gateway artifact URL or path names, whatever its origin (a package pins
// the public URL it was published under, while a download may arrive
// through another route to the same gateway); false for anything else.
func ArtifactKeyOf(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	key, onRoute := strings.CutPrefix(u.EscapedPath(), gatewayArtifactPrefix)
	if !onRoute || !digest.ValidKey(key) {
		return "", false
	}
	return key, true
}

func validateArtifactShapes(raw []byte) error {
	var shell struct {
		Artifacts map[string]json.RawMessage `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &shell); err != nil {
		return nil
	}
	for platform, rawArtifact := range shell.Artifacts {
		var artifact map[string]json.RawMessage
		if err := json.Unmarshal(rawArtifact, &artifact); err != nil {
			return fmt.Errorf("artifacts.%s requires url and sha256 strings; use {url:\"\", sha256:\"\"} or omit that artifact entry before upload", platform)
		}
		urlValue, hasURL := artifact["url"]
		shaValue, hasSHA := artifact["sha256"]
		if len(artifact) != 2 || !hasURL || !hasSHA || !jsonString(urlValue) || !jsonString(shaValue) {
			return fmt.Errorf("artifacts.%s requires url and sha256 strings; use {url:\"\", sha256:\"\"} or omit that artifact entry before upload", platform)
		}
	}
	return nil
}

func jsonString(raw json.RawMessage) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func validateAbsoluteHTTPURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("not absolute http(s)")
	}
	return nil
}

func validateArtifactMap(artifacts map[string]Artifact) error {
	for platform, artifact := range artifacts {
		if err := validateArtifact("artifacts."+platform, artifact); err != nil {
			return err
		}
	}
	return nil
}

// validateArtifact is the rule for one pinned artifact at its place in the
// package (Pins): an upload placeholder, or a url and a sha256.
func validateArtifact(at string, artifact Artifact) error {
	if isUploadPlaceholder(artifact) {
		return nil
	}
	if strings.TrimSpace(artifact.URL) == "" || strings.TrimSpace(artifact.SHA256) == "" {
		return fmt.Errorf("%s requires url and sha256 strings; use {url:\"\", sha256:\"\"} or omit that artifact entry before upload", at)
	}
	if !digest.Valid(artifact.SHA256) {
		return fmt.Errorf("%s.sha256 must be 64 lowercase hex characters", at)
	}
	return nil
}

func isUploadPlaceholder(artifact Artifact) bool {
	return artifact.URL == "" && artifact.SHA256 == ""
}

// ValidateGatewayArtifactURLs proves every concrete artifact URL came from the
// gateway artifact store. It intentionally ignores upload placeholders so local
// parsers can accept a draft package before upload, while publish-time callers
// can reject incomplete sidecar/plugin packages after this check.
func ValidateGatewayArtifactURLs(pkg Package, artifactBaseURL string) error {
	base, err := ParseGatewayArtifactBaseURL(artifactBaseURL)
	if err != nil {
		return err
	}
	for at, artifact := range pkg.Pins() {
		if isUploadPlaceholder(artifact) {
			continue
		}
		key, err := GatewayArtifactKey(artifact.URL, base)
		if err != nil {
			return fmt.Errorf("%s.url: %w", at, err)
		}
		if key != digest.Key(artifact.SHA256) {
			return fmt.Errorf("%s.url key must match sha256", at)
		}
	}
	return nil
}

func ParseGatewayArtifactBaseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("artifact base URL invalid")
	}
	return base, nil
}

// GatewayArtifactKey is the blob key of an absolute artifact URL that this
// gateway (origin base) published.
func GatewayArtifactKey(raw string, base *url.URL) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || !sameOrigin(u, base) {
		return "", fmt.Errorf("artifact URL must reference a gateway-uploaded artifact")
	}
	key, ok := ArtifactKeyOf(raw)
	if !ok {
		return "", fmt.Errorf("artifact URL must reference a gateway-uploaded artifact")
	}
	return key, nil
}

func sameOrigin(u, base *url.URL) bool {
	return strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Host, base.Host)
}

// CarriesArtifacts reports whether a runtime ships downloadable artifacts: a
// sidecar's or stdio MCP server's binaries, a previewer's bundle.
func (r Runtime) CarriesArtifacts() bool {
	return r == RuntimeSidecar || r == RuntimeBundle || r == RuntimeStdio
}

// ValidateCompleteArtifacts refuses a publish whose downloadable artifacts
// (a runtime's, a settings page's bundle) are still upload placeholders.
func ValidateCompleteArtifacts(pkg Package) error {
	for at, artifact := range pkg.Pins() {
		if at != SettingsPageBundle && !pkg.Runtime.CarriesArtifacts() {
			continue
		}
		if isUploadPlaceholder(artifact) {
			return fmt.Errorf("%s must be uploaded before publish", at)
		}
	}
	return nil
}
