package pkgmeta

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

const KindCorePackageV1 = "bnbloc.core.package/v1"

var semverRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
var packageNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type Runtime string

const (
	RuntimeEndpoint Runtime = "endpoint"
	RuntimeManifest Runtime = "manifest"
	RuntimeSidecar  Runtime = "sidecar"
)

type Package struct {
	Kind         string                 `json:"kind"`
	Name         string                 `json:"name"`
	Version      string                 `json:"version"`
	Protocol     protocol.ProtocolRange `json:"protocol"`
	Runtime      Runtime                `json:"runtime"`
	Endpoint     *Endpoint              `json:"endpoint,omitempty"`
	Manifest     *manifestcore.Manifest `json:"manifest,omitempty"`
	Sidecar      *Sidecar               `json:"sidecar,omitempty"`
	Artifacts    map[string]Artifact    `json:"artifacts,omitempty"`
	Capabilities []string               `json:"capabilities,omitempty"`
	Permissions  Permissions            `json:"permissions"`
	Publisher    string                 `json:"publisher"`
	Description  string                 `json:"description"`

	// Plugin-only payload: Contract is the typed plugin wire-contract version;
	// Hooks are versioned event hooks that must each exist in the code-owned
	// registry (package_validation.go). Both empty for a core package.
	Contract string   `json:"contract,omitempty"`
	Hooks    []string `json:"hooks,omitempty"`

	// Plugin-only payload: the settings a member fills in on their desktop
	// for this plugin (e.g. their own bot's credentials). The values never
	// leave the member's machine: the desktop stores them locally and hands
	// them to the sidecar at launch (docs/notifications.md, "Personal bots").
	LocalSettings []LocalSetting `json:"localSettings,omitempty"`

	// Sandbox-only payload: the guest profile of the one machine the desktop
	// prepares (prewarms) on a member's computer. The image is the
	// publisher's choice, never baked into the desktop. Optional: without it
	// the sidecar runs but no machine is prewarmed.
	Guest *Guest `json:"guest,omitempty"`

	// Previewer-only payload: the files a previewer bundle renders, its
	// bundle entry, rank, capabilities and toolbar (previewer_validation.go).
	Previewer *Previewer `json:"previewer,omitempty"`

	// MCP-only payload: the tools, env requirements, credentials source and
	// runtime details of an MCP tool component (mcp_validation.go).
	MCP *MCP `json:"mcp,omitempty"`
}

// Guest is a sandbox package's machine profile (Package.Guest).
type Guest struct {
	// Image is the OCI reference the machine boots (never a host path).
	Image     string `json:"image"`
	CPUs      int    `json:"cpus,omitempty"`
	MemoryMiB int    `json:"memoryMiB,omitempty"`
}

// LocalSetting is one field of a plugin's desktop-local settings schema.
// Secret values are write-only in the desktop UI (masked, never shown back).
type LocalSetting struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Help     string `json:"help,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type Endpoint struct {
	BaseURL string `json:"baseUrl"`
}

type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type Sidecar struct {
	ListenEnv string `json:"listenEnv,omitempty"`
	// SupervisedEnv names the env var the desktop sets to "1" at launch so the
	// sidecar knows it is supervised and exits when its stdin pipe closes.
	SupervisedEnv string   `json:"supervisedEnv,omitempty"`
	LocalURL      string   `json:"localUrl"`
	Args          []string `json:"args,omitempty"`
	// TokenEnv names the env var the desktop sets to a fresh random bearer
	// token at every launch. A sidecar declaring it must require that bearer
	// on every request; the desktop hands the same token to its local callers.
	TokenEnv string `json:"tokenEnv,omitempty"`
	// DataDirEnv names the env var the desktop sets to a per-component
	// private data directory it creates (owner-only) before launch.
	DataDirEnv string `json:"dataDirEnv,omitempty"`
	// Files are extra per-platform artifacts the sidecar needs beside its
	// executable (e.g. a VMM helper binary and a firmware library). Each is
	// published in Package.Artifacts under "<platform>/<name>", downloaded
	// and sha256-verified by the desktop exactly like the executable, and its
	// local path is handed to the sidecar in env Env.
	Files []SidecarFile `json:"files,omitempty"`
}

// SidecarFile declares one extra sidecar artifact (see Sidecar.Files).
type SidecarFile struct {
	Name string `json:"name"`
	Env  string `json:"env"`
	// Executable marks a file the desktop must mark executable (+x).
	Executable bool `json:"executable,omitempty"`
}

type Permissions struct {
	Network []string `json:"network,omitempty"`
	// Host lists the desktop plugin host API capabilities a plugin sidecar
	// may use (plugin packages only; PluginHostPermissions is the registry).
	// A plugin declaring any gets BNBLOC_PLUGIN_HOST_URL/_TOKEN at launch.
	Host []string `json:"host,omitempty"`
}

type TrustedKey struct {
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
}

type VerifiedPackage struct {
	Package     Package
	KeyID       string
	ContentHash string
}

func ParsePackage(raw []byte) (Package, error) {
	if err := validateArtifactShapes(raw); err != nil {
		return Package{}, err
	}
	var pkg Package
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pkg); err != nil {
		return Package{}, fmt.Errorf("package schema invalid: %w", err)
	}
	if err := validatePackage(pkg); err != nil {
		return Package{}, err
	}
	return pkg, nil
}

func VerifySignedPackage(body, signature []byte, trusted []TrustedKey) (VerifiedPackage, error) {
	if len(strings.TrimSpace(string(signature))) == 0 {
		return VerifiedPackage{}, fmt.Errorf("signature missing")
	}
	decodedSig, err := decodeMinisignSignature(signature)
	if err != nil {
		return VerifiedPackage{}, err
	}
	pub, err := trustedPublicKey(decodedSig.KeyID, trusted)
	if err != nil {
		return VerifiedPackage{}, err
	}
	if !ed25519.Verify(pub, body, decodedSig.Signature) {
		return VerifiedPackage{}, fmt.Errorf("signature verification failed for trusted key %s", decodedSig.KeyID)
	}
	pkg, err := ParsePackage(body)
	if err != nil {
		return VerifiedPackage{}, err
	}
	hash := sha256.Sum256(body)
	return VerifiedPackage{Package: pkg, KeyID: decodedSig.KeyID, ContentHash: hex.EncodeToString(hash[:])}, nil
}

// kindValidators holds the per-kind payload validator. The kind-neutral shell
// (name/version/publisher/artifacts) is validated for every kind; the
// kind-specific payload (a core's protocol/runtime/manifest/capabilities) is
// delegated here so a new kind plugs in without editing the shell. Populated by
// RegisterKindValidator (the core kind registers itself in init).
var kindValidators = map[string]func(Package) error{}

// RegisterKindValidator registers a package kind's payload validator. A kind
// with no validator is rejected at parse time (unknown kind).
func RegisterKindValidator(kind string, v func(Package) error) {
	kindValidators[kind] = v
}

// ValidatePackage validates a constructed Package exactly as ParsePackage
// validates a decoded one: the kind-neutral shell, then the kind's payload.
// Package-source tooling (which builds a Package rather than parsing bytes)
// uses it so it never re-implements — and drifts from — the install gate.
func ValidatePackage(pkg Package) error { return validatePackage(pkg) }

// ValidatePackageShell validates only the kind-neutral shell (name, semver
// version, publisher, artifact map). It is for package-source material whose
// kind has no installable payload validator yet (a contract fixture), so even
// that material is held to the same naming/versioning rules.
func ValidatePackageShell(pkg Package) error { return validatePackageShell(pkg) }

func validatePackage(pkg Package) error {
	if err := validatePackageShell(pkg); err != nil {
		return err
	}
	validate, ok := kindValidators[pkg.Kind]
	if !ok {
		return fmt.Errorf("unknown package kind %q", pkg.Kind)
	}
	if pkg.Kind != KindPluginPackageV1 {
		if len(pkg.LocalSettings) > 0 {
			return fmt.Errorf("localSettings is only valid for %s packages", KindPluginPackageV1)
		}
		if len(pkg.Permissions.Host) > 0 {
			return fmt.Errorf("permissions.host is only valid for %s packages", KindPluginPackageV1)
		}
	}
	if pkg.Guest != nil && pkg.Kind != KindSandboxPackageV1 {
		return fmt.Errorf("guest is only valid for %s packages", KindSandboxPackageV1)
	}
	if pkg.Previewer != nil && pkg.Kind != KindPreviewerPackageV1 {
		return fmt.Errorf("previewer is only valid for %s packages", KindPreviewerPackageV1)
	}
	if pkg.MCP != nil && pkg.Kind != KindMCPPackageV1 {
		return fmt.Errorf("mcp is only valid for %s packages", KindMCPPackageV1)
	}
	if err := validateSidecarShape(pkg); err != nil {
		return err
	}
	return validate(pkg)
}

func validatePackageShell(pkg Package) error {
	if strings.TrimSpace(pkg.Name) == "" {
		return fmt.Errorf("package name required")
	}
	if !packageNameRE.MatchString(pkg.Name) {
		return fmt.Errorf("package name must use lowercase letters, digits, dot, underscore, or dash")
	}
	if !semverRE.MatchString(pkg.Version) {
		return fmt.Errorf("package version must be semver")
	}
	if strings.TrimSpace(pkg.Publisher) == "" {
		return fmt.Errorf("publisher required")
	}
	return validateArtifactMap(pkg.Artifacts)
}

// validateCapabilities rejects any capability not in the Core Protocol set. It
// is the one capability gate for every package kind (core and plugin), applied
// by the shared shell so a kind validator never re-checks it.
func validateCoreCapabilities(caps []string) error {
	for _, cap := range caps {
		switch cap {
		case protocol.CapabilityInterrupt, protocol.CapabilityWriteFile, protocol.CapabilityDeleteWorkspace, protocol.CapabilityDiscover:
		default:
			return fmt.Errorf("unknown capability %q", cap)
		}
	}
	return nil
}

// validateCorePackage is the core kind's payload validator (registered in init).
func validateCorePackage(pkg Package) error {
	if err := validateCoreCapabilities(pkg.Capabilities); err != nil {
		return err
	}
	if err := compatibleProtocolRange("core protocol", protocol.ProtocolRange{Min: protocol.SupportedMinVersion, Max: protocol.SupportedMaxVersion}, pkg.Protocol); err != nil {
		return err
	}
	switch pkg.Runtime {
	case RuntimeEndpoint:
		if len(pkg.Artifacts) > 0 {
			return fmt.Errorf(`runtime "endpoint" must not declare artifacts`)
		}
		if pkg.Endpoint == nil || strings.TrimSpace(pkg.Endpoint.BaseURL) == "" {
			return fmt.Errorf(`runtime "endpoint" requires endpoint.baseUrl`)
		}
		if err := validateAbsoluteHTTPURL(pkg.Endpoint.BaseURL); err != nil {
			return fmt.Errorf("endpoint.baseUrl must be an absolute http(s) URL")
		}
	case RuntimeManifest:
		if len(pkg.Artifacts) > 0 {
			return fmt.Errorf(`runtime "manifest" must not declare artifacts`)
		}
		if pkg.Manifest == nil {
			return fmt.Errorf(`runtime "manifest" requires manifest`)
		}
		if pkg.Endpoint != nil && strings.TrimSpace(pkg.Endpoint.BaseURL) != "" {
			if err := validateAbsoluteHTTPURL(pkg.Endpoint.BaseURL); err != nil {
				return fmt.Errorf("endpoint.baseUrl must be an absolute http(s) URL")
			}
		}
		if _, err := manifestcore.New(*pkg.Manifest); err != nil {
			return fmt.Errorf("manifest invalid: %w", err)
		}
	case RuntimeSidecar:
		if len(pkg.Artifacts) == 0 {
			return fmt.Errorf(`runtime "sidecar" requires artifacts`)
		}
		// A sidecar speaks Core Protocol v1 at its loopback URL and
		// self-describes via /core/v1/discover, so a drive manifest is not
		// required. When one IS present (a legacy REST sidecar driven through
		// manifest templates) it must still be valid.
		if pkg.Manifest != nil {
			if _, err := manifestcore.New(*pkg.Manifest); err != nil {
				return fmt.Errorf("manifest invalid: %w", err)
			}
		}
		if pkg.Sidecar == nil || strings.TrimSpace(pkg.Sidecar.LocalURL) == "" {
			return fmt.Errorf(`runtime "sidecar" requires sidecar.localUrl`)
		}
		if err := ValidateLoopbackHTTPURL(pkg.Sidecar.LocalURL); err != nil {
			return fmt.Errorf("sidecar.localUrl must be a loopback http URL")
		}
	default:
		return fmt.Errorf("unknown runtime %q", pkg.Runtime)
	}
	return nil
}

func init() { RegisterKindValidator(KindCorePackageV1, validateCorePackage) }

// compatibleProtocolRange checks that a package's declared protocol range
// overlaps the local [min,max] of the protocol its kind speaks (Core Protocol
// for cores, Sandbox Protocol for sandboxes). label names that protocol in
// the mismatch error.
func compatibleProtocolRange(label string, local, remote protocol.ProtocolRange) error {
	localMin, err := protocol.ParseVersion(local.Min)
	if err != nil {
		return err
	}
	localMax, err := protocol.ParseVersion(local.Max)
	if err != nil {
		return err
	}
	remoteMin, err := protocol.ParseVersion(remote.Min)
	if err != nil {
		return fmt.Errorf("remote min: %w", err)
	}
	remoteMax, err := protocol.ParseVersion(remote.Max)
	if err != nil {
		return fmt.Errorf("remote max: %w", err)
	}
	if compareProtocolVersion(remoteMax, localMin) < 0 || compareProtocolVersion(remoteMin, localMax) > 0 {
		return &protocol.VersionMismatchError{Protocol: label, LocalMin: local.Min, LocalMax: local.Max, RemoteMin: remote.Min, RemoteMax: remote.Max}
	}
	return nil
}

func compareProtocolVersion(a, b protocol.Version) int {
	if a.Major != b.Major {
		return a.Major - b.Major
	}
	return a.Minor - b.Minor
}

// ValidateLoopbackHTTPURL accepts only an absolute http URL on a loopback
// host: a sidecar listens on the machine of the desktop that supervises it.
func ValidateLoopbackHTTPURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return fmt.Errorf("not absolute http loopback")
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" {
		return fmt.Errorf("host is not loopback")
	}
	return nil
}

func validSHA256(raw string) bool {
	if len(raw) != 64 || strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}
