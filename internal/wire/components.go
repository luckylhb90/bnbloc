package wire

// Component projections: installed executable components as the platform
// console and the member catalog show them (the gateway's /v1/platform/
// components and /v1/components). The package parts they carry are the
// shapes in component_shapes.go.

// ComponentProtocolRange is the Core Protocol range a core or sandbox
// package was published for (core/protocol.ProtocolRange).
type ComponentProtocolRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

// ComponentTrust is how an installed component was verified: the publisher
// key that signed it and the hash of the package it installed.
type ComponentTrust struct {
	KeyID       string `json:"keyId,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	Signed      bool   `json:"signed"`
}

// ComponentAdoption counts the members who opted in to a component.
type ComponentAdoption struct {
	MemberInstalls int `json:"memberInstalls"`
	TotalMembers   int `json:"totalMembers,omitempty"`
}

// PlatformComponent is one installed component in the platform console
// (GET /v1/platform/components).
type PlatformComponent struct {
	Kind          string                       `json:"kind"`
	Name          string                       `json:"name"`
	Version       string                       `json:"version"`
	Publisher     string                       `json:"publisher"`
	Runtime       string                       `json:"runtime" tstype:"ComponentRuntime"`
	Contract      string                       `json:"contract,omitempty"`
	Protocol      ComponentProtocolRange       `json:"protocol"`
	KeyID         string                       `json:"keyId"`
	ContentHash   string                       `json:"contentHash"`
	InstalledAtMs int64                        `json:"installedAtMs"`
	Description   string                       `json:"description,omitempty"`
	Capabilities  []string                     `json:"capabilities,omitempty"`
	Permissions   ComponentPermissions         `json:"permissions"`
	Hooks         []string                     `json:"hooks,omitempty"`
	Settings      []ComponentSetting           `json:"settings,omitempty"`
	SettingsPage  *ComponentSettingsPage       `json:"settingsPage,omitempty"`
	Artifacts     map[string]ComponentArtifact `json:"artifacts,omitempty"`
	Endpoint      *ComponentEndpoint           `json:"endpoint,omitempty"`
	Sidecar       *ComponentSidecar            `json:"sidecar,omitempty"`
	// RequiresDesktop marks a component that runs on each member's desktop
	// (a sidecar other than an auth provider, a stdio MCP server).
	RequiresDesktop bool `json:"requiresDesktop,omitempty"`
	// Manifest is a manifest core's interpreted manifest
	// (core/manifestcore.Manifest), shown as published.
	Manifest  any                 `json:"manifest,omitempty"`
	Previewer *ComponentPreviewer `json:"previewer,omitempty"`
	MCP       *ComponentMCP       `json:"mcp,omitempty"`
	Rollout   string              `json:"rollout" tstype:"ComponentRollout"`
	Trust     ComponentTrust      `json:"trust"`
	Adoption  ComponentAdoption   `json:"adoption"`
}

// PlatformComponentsReply is GET /v1/platform/components.
type PlatformComponentsReply struct {
	Components []PlatformComponent `json:"components"`
}

// PublishedComponent answers an install: the installed component, and a
// notifier's signing secret on its first install (shown this once).
type PublishedComponent struct {
	PlatformComponent
	Secret string `json:"secret,omitempty"`
}

// ComponentInstallRequest is POST /v1/platform/components: a package and
// its detached signature, as text.
type ComponentInstallRequest struct {
	Package   string `json:"package"`
	Signature string `json:"signature"`
	Rollout   string `json:"rollout,omitempty" tstype:"ComponentRollout"`
}

// ComponentRolloutRequest sets an installed component's rollout.
type ComponentRolloutRequest struct {
	Rollout string `json:"rollout" tstype:"ComponentRollout"`
}

// MemberComponent is one component of the member catalog (GET
// /v1/components): what the member may use, and, once installed for them,
// what their desktop needs to run it.
type MemberComponent struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Publisher       string   `json:"publisher,omitempty"`
	Description     string   `json:"description,omitempty"`
	Runtime         string   `json:"runtime,omitempty" tstype:"ComponentRuntime"`
	Contract        string   `json:"contract,omitempty"`
	RequiresDesktop bool     `json:"requiresDesktop,omitempty"`
	Hooks           []string `json:"hooks,omitempty"`
	// Permissions tells the member's desktop how to launch a plugin
	// sidecar: the host API capabilities it may use.
	Permissions *ComponentPermissions `json:"permissions,omitempty"`
	// Settings is the component's declaration (docs/prd/2026-10-19-
	// component-settings.md). SettingsSet are the keys of its platform and
	// organization settings that hold a value for the member's
	// organization (values never come here), and SettingsMissing the
	// required ones of those scopes that are not set: while any is, the
	// component needs setup and does not run. Member-scope settings live on
	// the member's desktop. SettingsPage is the component's own settings
	// page, shown instead of the one form.
	Settings        []ComponentSetting           `json:"settings,omitempty"`
	SettingsSet     []string                     `json:"settingsSet,omitempty"`
	SettingsMissing []string                     `json:"settingsMissing,omitempty"`
	SettingsPage    *ComponentSettingsPage       `json:"settingsPage,omitempty"`
	Artifacts       map[string]ComponentArtifact `json:"artifacts,omitempty"`
	Endpoint        *ComponentEndpoint           `json:"endpoint,omitempty"`
	Sidecar         *ComponentSidecar            `json:"sidecar,omitempty"`
	// Capabilities and Protocol tell the desktop/bridge what a protocol
	// component (a sandbox runtime) was published to support before it is
	// launched; the running implementation's discovery stays authoritative.
	Capabilities  []string                `json:"capabilities,omitempty"`
	Protocol      *ComponentProtocolRange `json:"protocol,omitempty"`
	Rollout       string                  `json:"rollout" tstype:"ComponentRollout"`
	Installed     bool                    `json:"installed"`
	Managed       bool                    `json:"managed"`
	InstalledAtMs int64                   `json:"installedAtMs,omitempty"`
	// PublishedAtMs is when the platform installed this version.
	PublishedAtMs int64 `json:"publishedAtMs,omitempty"`
	// MemberInstalls counts the members who opted in (the platform list's
	// adoption.memberInstalls).
	MemberInstalls int `json:"memberInstalls,omitempty"`
	// Guest is a sandbox's machine profile: the desktop prewarms it.
	Guest *ComponentGuest `json:"guest,omitempty"`
	// Previewer is an installed previewer's match/toolbar metadata, its
	// pinned bundle, and the caller's browser frame URL.
	Previewer *MemberComponentPreviewer `json:"previewer,omitempty"`
	// MCP is an MCP tool's tools, credentials source and transport.
	MCP *MemberComponentMCP `json:"mcp,omitempty"`
}

// MemberComponentPreviewer is an installed previewer in the member catalog:
// its package payload, its pinned bundle and the caller's frame.
type MemberComponentPreviewer struct {
	ComponentPreviewer
	// BundleURL is the gateway artifact URL of the archive (the desktop
	// downloads it and checks BundleSHA256).
	BundleURL    string `json:"bundleUrl"`
	BundleSHA256 string `json:"bundleSha256"`
	// FrameURL is the gateway-relative browser frame base (ticket included);
	// the host appends Entry.
	FrameURL         string `json:"frameUrl"`
	FrameExpiresAtMs int64  `json:"frameExpiresAtMs"`
}

// MemberComponentMCP is an installed MCP tool in the member catalog: its
// package payload and how it is reached.
type MemberComponentMCP struct {
	ComponentMCP
	Transport string `json:"transport"`
}

// ComponentsReply is GET /v1/components: the executable catalog the desktop
// supervises and the content components (experts, skill packs) the member
// may use.
type ComponentsReply struct {
	Components        []MemberComponent  `json:"components"`
	ContentComponents []ContentComponent `json:"contentComponents"`
}

// ComponentKey is one trusted publisher key (GET /v1/platform/component-keys).
type ComponentKey struct {
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
	Label     string `json:"label,omitempty"`
	AddedAtMs int64  `json:"addedAtMs,omitempty"`
	AddedBy   string `json:"addedBy,omitempty"`
}

// ComponentKeysReply lists the trusted publisher keys, and whether this
// gateway refuses unsigned packages (the publishing UI demands a signature
// exactly when the install would).
type ComponentKeysReply struct {
	Keys              []ComponentKey `json:"keys"`
	SignatureRequired bool           `json:"signatureRequired"`
}
