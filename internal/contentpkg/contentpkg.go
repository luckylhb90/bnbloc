// Package contentpkg is the one validator, builder and importer of content
// components (docs/prd/2026-10-12-experts-and-teams.md): packages that are
// mostly text — experts, skill packs and prompt packs — which members
// author as well as the platform. Install, member publish, form
// saves, imports and the bridge's materialization all go through it, so a
// package is checked by the same rules wherever it enters.
//
// A content package is a plain ustar bundle (internal/tarbundle) holding a
// component.json manifest and the kind's files. Each kind registers a
// KindSpec: its tarbundle limit profile and its own checks; the shared
// envelope (name, semver version, display data, requires, permissions) is
// checked here once for all kinds.
package contentpkg

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// Content kinds.
const (
	KindExpert = "bnbloc.expert.package/v1"
	KindSkill  = "bnbloc.skill.package/v1"
	KindPrompt = "bnbloc.prompt.package/v1"
)

// ManifestFile is the manifest's path in every content bundle.
const ManifestFile = "component.json"

// Permissions a content package declares (requires.permissions). They are
// shown on its card and confirmed on first use; content that is not the
// member's own and declares scripts or mcp.stdio forces a sandboxed drive.
const (
	PermScripts  = "scripts"
	PermMCPStdio = "mcp.stdio"
	// PermMCPHTTPPrefix is followed by the host the package's MCP tool
	// reaches ("mcp.http:tools.example.com").
	PermMCPHTTPPrefix = "mcp.http:"
)

// Text bounds of the envelope.
const (
	maxName         = 63
	maxDisplayName  = 120
	maxDescription  = 1000
	maxCategory     = 40
	maxTags         = 12
	maxTag          = 32
	maxIcon         = 64
	maxRequires     = 32
	maxInstructions = 64 << 10
	maxQuickPrompts = 12
	maxQuickTitle   = 80
	maxQuickPrompt  = 4000
	maxWelcome      = 4000
	maxSkillDesc    = 1024
	maxSkillsInPack = 64
)

var (
	nameRE = regexp.MustCompile(fmt.Sprintf(`^[a-z0-9][a-z0-9-]{0,%d}$`, maxName-1))
	tagRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9 _-]*$`)
	hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)
)

// ValidName reports whether s is a valid component or skill name: lower-case
// letters, digits and hyphens, starting with a letter or digit, at most
// maxName.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// Manifest is component.json, the envelope shared by every content kind
// plus the kind-specific fields (each kind's validator refuses the fields
// of other kinds).
type Manifest struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	DisplayName string   `json:"displayName,omitempty"`
	Description string   `json:"description,omitempty"`
	Category    string   `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Requires    Requires `json:"requires,omitempty"`

	// Expert: the quick prompts and welcome card shown in the composer and
	// the room (INSTRUCTIONS.md holds the always-on part).
	QuickPrompts []QuickPrompt `json:"quickPrompts,omitempty"`
	Welcome      string        `json:"welcome,omitempty"`
	// Team makes the expert a team: the lead wears it (INSTRUCTIONS.md is
	// the SOP) and dispatches to the roster's roles (team.go).
	Team *TeamSpec `json:"team,omitempty"`

	// Skill pack: the slugs of its skills (<slug>/SKILL.md each).
	Skills []string `json:"skills,omitempty"`

	// Prompt pack: its prompts (they live in the manifest).
	Prompts []Prompt `json:"prompts,omitempty"`
}

// Requires lists what a package needs at use time: skill and prompt packs
// by "name@range", MCP tools by name (with the env names their credentials
// come from), and the permissions it declares.
type Requires struct {
	Skills      []string         `json:"skills,omitempty"`
	Prompts     []string         `json:"prompts,omitempty"`
	MCP         []MCPRequirement `json:"mcp,omitempty"`
	Permissions []string         `json:"permissions,omitempty"`
}

// MCPRequirement names an MCP tool component and the env variable names its
// credentials are read from (values never ship in a package).
type MCPRequirement struct {
	Name string   `json:"name"`
	Env  []string `json:"env,omitempty"`
}

// QuickPrompt is one composer quick prompt of an expert: its text, or a
// reference to a prompt of a prompt pack the expert requires
// ("pack/prompt-id"; the pack is named in requires.prompts).
type QuickPrompt struct {
	Title  string `json:"title,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Ref    string `json:"ref,omitempty"`
}

// Reference is a parsed "name@range" requirement.
type Reference struct {
	Name  string
	Range string // "" = any release
}

// ParseReference parses "name" or "name@range".
func ParseReference(s string) (Reference, error) {
	name, rng, _ := strings.Cut(strings.TrimSpace(s), "@")
	if !ValidName(name) {
		return Reference{}, fmt.Errorf("invalid reference %q (want name@range)", s)
	}
	return Reference{Name: name, Range: strings.TrimSpace(rng)}, nil
}

// KindSpec is one registered content kind.
type KindSpec struct {
	// Kind is the manifest kind string.
	Kind string
	// Short is the API / UI name ("expert", "skill", "prompt").
	Short string
	// Limits is the kind's tarbundle limit profile.
	Limits tarbundle.Limits
	// MemberUse reports a kind a member turns on for themselves (a
	// content use of member scope): skill packs (all their agents) and
	// prompt packs (their composer). Experts are picked per message.
	MemberUse bool
	// validate checks the kind's own manifest fields and files.
	validate func(p *Package) error
	// fromForm lays out the kind's files from a form save.
	fromForm func(f Form, m *Manifest, files map[string][]byte) error
	// fromImport generates the kind's manifest fields and files from an
	// imported foreign format (files of the upload; m carries the kind and
	// version, and the name when one was given).
	fromImport func(files map[string][]byte, m *Manifest, out map[string][]byte) error
}

var kinds = map[string]KindSpec{}

func register(k KindSpec) {
	if _, dup := kinds[k.Kind]; dup {
		panic("contentpkg: duplicate kind " + k.Kind)
	}
	kinds[k.Kind] = k
	// A content kind is also a pkgmeta package kind: its signed platform
	// package (platform.go) installs through the component pipeline.
	pkgmeta.RegisterKindValidator(k.Kind, validatePlatformPackage)
}

// Lookup returns the registered kind by manifest kind or short name.
func Lookup(kind string) (KindSpec, bool) {
	kind = strings.TrimSpace(kind)
	if k, ok := kinds[kind]; ok {
		return k, true
	}
	for _, k := range kinds {
		if k.Short == kind {
			return k, true
		}
	}
	return KindSpec{}, false
}

// Kinds lists the registered kinds (sorted by short name).
func Kinds() []KindSpec {
	out := make([]KindSpec, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Short < out[j].Short })
	return out
}
