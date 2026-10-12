package gateway

import (
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

type componentInstallRequest struct {
	Package   string `json:"package"`
	Signature string `json:"signature"`
	Rollout   string `json:"rollout,omitempty"`
}

type installedComponent struct {
	Kind            string                      `json:"kind"`
	Name            string                      `json:"name"`
	Version         string                      `json:"version"`
	Publisher       string                      `json:"publisher"`
	Runtime         pkgmeta.Runtime             `json:"runtime"`
	Contract        string                      `json:"contract,omitempty"`
	Protocol        protocol.ProtocolRange      `json:"protocol"`
	KeyID           string                      `json:"keyId"`
	ContentHash     string                      `json:"contentHash"`
	InstalledAtMs   int64                       `json:"installedAtMs"`
	Description     string                      `json:"description,omitempty"`
	Capabilities    []string                    `json:"capabilities,omitempty"`
	Permissions     pkgmeta.Permissions         `json:"permissions"`
	Hooks           []string                    `json:"hooks,omitempty"`
	LocalSettings   []pkgmeta.LocalSetting      `json:"localSettings,omitempty"`
	Artifacts       map[string]pkgmeta.Artifact `json:"artifacts,omitempty"`
	Endpoint        *pkgmeta.Endpoint           `json:"endpoint,omitempty"`
	Sidecar         *pkgmeta.Sidecar            `json:"sidecar,omitempty"`
	RequiresDesktop bool                        `json:"requiresDesktop,omitempty"`
	Manifest        *manifestcore.Manifest      `json:"manifest,omitempty"`
	Previewer       *pkgmeta.Previewer          `json:"previewer,omitempty"`
	MCP             *pkgmeta.MCP                `json:"mcp,omitempty"`
	Rollout         string                      `json:"rollout"`
	Trust           componentTrust              `json:"trust"`
	Adoption        componentAdoption           `json:"adoption"`
}

type componentTrust struct {
	KeyID       string `json:"keyId,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	Signed      bool   `json:"signed"`
}

type componentAdoption struct {
	MemberInstalls int `json:"memberInstalls"`
	TotalMembers   int `json:"totalMembers,omitempty"`
}

type setRolloutRequest struct {
	Rollout string `json:"rollout"`
}
