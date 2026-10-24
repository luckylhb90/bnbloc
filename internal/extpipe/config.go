package extpipe

import (
	"encoding/json"
	"fmt"

	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

// PackageConfig is the one persisted projection of a verified package: the
// kind-specific half of an install record (Record carries the identity half —
// name, version, publisher, signature, network). Every kind stores this same
// shape in Record.Config, so the gateway catalog, the plugin runtime and the
// auth-provider runtime decode one type instead of each keeping a near-copy
// that drifts. Fields a kind does not use are simply empty.
type PackageConfig struct {
	Runtime      pkgmeta.Runtime             `json:"runtime"`
	Contract     string                      `json:"contract,omitempty"`
	Protocol     protocol.ProtocolRange      `json:"protocol"`
	Endpoint     *pkgmeta.Endpoint           `json:"endpoint,omitempty"`
	Manifest     *manifestcore.Manifest      `json:"manifest,omitempty"`
	Sidecar      *pkgmeta.Sidecar            `json:"sidecar,omitempty"`
	Artifacts    map[string]pkgmeta.Artifact `json:"artifacts,omitempty"`
	Capabilities []string                    `json:"capabilities,omitempty"`
	Hooks        []string                    `json:"hooks,omitempty"`
	Permissions  pkgmeta.Permissions         `json:"permissions"`
	Description  string                      `json:"description,omitempty"`
	// Settings is the component's settings declaration (plugins, MCP tools,
	// notifiers and auth providers),
	// SettingsPage its own page for them.
	Settings     []pkgmeta.Setting     `json:"settings,omitempty"`
	SettingsPage *pkgmeta.SettingsPage `json:"settingsPage,omitempty"`
	// Panel is a plugin's own page for its sidecar.
	Panel *pkgmeta.Page `json:"panel,omitempty"`
	// Guest is a sandbox's machine profile (the image the desktop prewarms).
	Guest *pkgmeta.Guest `json:"guest,omitempty"`
	// Previewer is a previewer's match/entry/rank/capabilities/toolbar.
	Previewer *pkgmeta.Previewer `json:"previewer,omitempty"`
	// MCP is an MCP tool's tools, credentials source and runtime details.
	MCP *pkgmeta.MCP `json:"mcp,omitempty"`
}

// ConfigFromPackage projects a verified package onto its persisted config.
func ConfigFromPackage(pkg pkgmeta.Package) PackageConfig {
	return PackageConfig{
		Runtime:      pkg.Runtime,
		Contract:     pkg.Contract,
		Protocol:     pkg.Protocol,
		Endpoint:     pkg.Endpoint,
		Manifest:     pkg.Manifest,
		Sidecar:      pkg.Sidecar,
		Artifacts:    pkg.Artifacts,
		Capabilities: append([]string(nil), pkg.Capabilities...),
		Hooks:        append([]string(nil), pkg.Hooks...),
		Permissions:  pkg.Permissions,
		Description:  pkg.Description,
		Settings:     append([]pkgmeta.Setting(nil), pkg.Settings...),
		SettingsPage: pkg.SettingsPage,
		Panel:        pkg.Panel,
		Guest:        pkg.Guest,
		Previewer:    pkg.Previewer,
		MCP:          pkg.MCP,
	}
}

// EncodePackageConfig projects a package and marshals it for Record.Config.
func EncodePackageConfig(pkg pkgmeta.Package) (json.RawMessage, error) {
	return json.Marshal(ConfigFromPackage(pkg))
}

// DecodeConfig decodes a record's persisted config. An empty config decodes to
// the zero value (records of kinds with no payload).
func DecodeConfig(rec Record) (PackageConfig, error) {
	var cfg PackageConfig
	if len(rec.Config) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(rec.Config, &cfg); err != nil {
		return PackageConfig{}, fmt.Errorf("decode %s/%s config: %w", rec.Kind, rec.Name, err)
	}
	return cfg, nil
}

// Package reassembles the package a record was installed from (identity from
// the record, payload from its config), e.g. to re-probe or re-activate it.
func (c PackageConfig) Package(kind string, rec Record) pkgmeta.Package {
	return pkgmeta.Package{
		Kind:         kind,
		Name:         rec.Name,
		Version:      rec.Version,
		Protocol:     c.Protocol,
		Runtime:      c.Runtime,
		Endpoint:     c.Endpoint,
		Manifest:     c.Manifest,
		Sidecar:      c.Sidecar,
		Artifacts:    c.Artifacts,
		Capabilities: c.Capabilities,
		Permissions:  c.EffectivePermissions(rec),
		Publisher:    rec.Publisher,
		Description:  c.Description,
		Contract:     c.Contract,
		Hooks:        c.Hooks,
		Settings:     c.Settings,
		SettingsPage: c.SettingsPage,
		Panel:        c.Panel,
		Guest:        c.Guest,
		Previewer:    c.Previewer,
		MCP:          c.MCP,
	}
}

// EffectivePermissions is the permission set that governs the record: the
// package's declared permissions, the network allowlist falling back to the
// record's copy.
func (c PackageConfig) EffectivePermissions(rec Record) pkgmeta.Permissions {
	p := c.Permissions
	if len(p.Network) == 0 {
		p.Network = rec.Network
	}
	return p
}
