package pkgmeta

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const KindCorePackageV1 = "bnbloc.core.package/v1"

var packageNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidPackageName is the package name rule: lower-case letters, digits,
// dot, underscore and dash, starting with a letter or digit.
func ValidPackageName(name string) bool { return packageNameRE.MatchString(name) }

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

	// Settings the component declares (settings.go,
	// docs/prd/2026-10-19-component-settings.md): who sets each, for whom,
	// and how its value reaches the component. Plugins and MCP tools only
	// (the field-to-kinds table).
	Settings []Setting `json:"settings,omitempty"`
	// SettingsPage is the component's own page for its settings, shown
	// instead of the one form (settings_page.go). Only beside Settings.
	SettingsPage *SettingsPage `json:"settingsPage,omitempty"`
	// Panel is a plugin's own page for its sidecar, shown from its card on
	// the member's desktop (docs/architecture/plugin-panels.md).
	Panel *Page `json:"panel,omitempty"`

	// Sandbox-only payload: the guest profile of the one machine the desktop
	// prepares (prewarms) on a member's computer. The image is the
	// publisher's choice, never baked into the desktop. Optional: without it
	// the sidecar runs but no machine is prewarmed.
	Guest *Guest `json:"guest,omitempty"`

	// Previewer-only payload: the files a previewer bundle renders, its
	// bundle entry, rank, capabilities and toolbar (previewer_validation.go).
	Previewer *Previewer `json:"previewer,omitempty"`

	// MCP-only payload: the tools, credentials source and runtime details
	// of an MCP tool component (mcp_validation.go).
	MCP *MCP `json:"mcp,omitempty"`
}

// The parts of a package the gateway also shows in its component
// projections. internal/wire owns their JSON (component_shapes.go, from
// which the web's types are generated); these are their names for
// package authors, who cannot import an internal package.
type (
	Endpoint             = wire.ComponentEndpoint
	Artifact             = wire.ComponentArtifact
	Permissions          = wire.ComponentPermissions
	Sidecar              = wire.ComponentSidecar
	SidecarFile          = wire.ComponentSidecarFile
	Guest                = wire.ComponentGuest
	Previewer            = wire.ComponentPreviewer
	PreviewerMatch       = wire.FileMatch
	PreviewerToolbarItem = wire.ComponentPreviewerToolbarItem
	MCP                  = wire.ComponentMCP
	MCPTool              = wire.ComponentMCPTool
	MCPStdio             = wire.ComponentMCPStdio
	MCPHTTP              = wire.ComponentMCPHTTP
	MCPHTTPAuth          = wire.ComponentMCPHTTPAuth
)

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
	return VerifiedPackage{Package: pkg, KeyID: decodedSig.KeyID, ContentHash: digest.Of(body)}, nil
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
	if err := validateKindFields(pkg); err != nil {
		return err
	}
	if err := validateSidecarShape(pkg); err != nil {
		return err
	}
	if err := validate(pkg); err != nil {
		return err
	}
	if err := validateSettings(pkg); err != nil {
		return err
	}
	if err := validateSettingsPage(pkg); err != nil {
		return err
	}
	return validatePanel(pkg)
}

func validatePackageShell(pkg Package) error {
	if strings.TrimSpace(pkg.Name) == "" {
		return fmt.Errorf("package name required")
	}
	if !ValidPackageName(pkg.Name) {
		return fmt.Errorf("package name must use lowercase letters, digits, dot, underscore, or dash")
	}
	if !semver.Valid(pkg.Version) {
		return fmt.Errorf("package version must be semver")
	}
	if strings.TrimSpace(pkg.Publisher) == "" {
		return fmt.Errorf("publisher required")
	}
	return validateArtifactMap(pkg.Artifacts)
}

// validateCoreCapabilities rejects any capability the Core Protocol does not
// define. protocol.KnownCapability is the one list, so a package built from
// a daemon's discovery document declares exactly what the runtime
// negotiates.
func validateCoreCapabilities(caps []string) error {
	for _, cap := range caps {
		if !protocol.KnownCapability(cap) {
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
	// The runtime's own negotiation rule (protocol.CompatibleRange): an
	// installable package is one the runtime will talk to.
	if err := protocol.Compatible(pkg.Protocol); err != nil {
		return err
	}
	switch pkg.Runtime {
	case RuntimeEndpoint:
		return RequireEndpointRuntime(pkg, "core")
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
		if err := requireSidecarRuntime(pkg, "core"); err != nil {
			return err
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
	default:
		return fmt.Errorf("unknown runtime %q", pkg.Runtime)
	}
	return nil
}

func init() { RegisterKindValidator(KindCorePackageV1, validateCorePackage) }

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
