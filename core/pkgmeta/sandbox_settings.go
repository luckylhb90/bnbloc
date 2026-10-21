package pkgmeta

import (
	"fmt"
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/wire"
	sandboxprotocol "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// The settings of a sandbox component the platform reads
// (docs/prd/2026-10-21-sandbox-private-hosts.md §2.4; the keys are
// wire.SandboxSetting*): a sandbox package declares them in its own
// `settings` like any component, and they are set in Components through the
// one settings dialog. Their keys are the kind's contract, as an assist
// task's fields are its callers': the desktop reads them by these keys (a
// copy pinned by testdata/sandbox_settings.json), and a package declaring
// one declares it in this shape. A package may declare more, and fewer: an
// undeclared one takes the platform's default.
const (
	SandboxSettingMode         = wire.SandboxSettingMode
	SandboxSettingForwardEnv   = wire.SandboxSettingForwardEnv
	SandboxSettingPrivateHosts = wire.SandboxSettingPrivateHosts
	SandboxSettingOrgHosts     = wire.SandboxSettingOrgHosts
	SandboxSettingMemberHosts  = wire.SandboxSettingMemberHosts
)

// The modes (SandboxSettingMode's options) and the member-hosts choices.
const (
	SandboxModeOff      = "off"
	SandboxModeBorrowed = "borrowed"
	SandboxModeAll      = "all"

	SandboxMemberHostsAsk = "ask"
	SandboxMemberHostsOff = "off"
)

// sandboxSettingShape is one contract key's type, scope and options.
type sandboxSettingShape struct {
	typ, scope string
	options    []string
}

var sandboxSettingShapes = map[string]sandboxSettingShape{
	SandboxSettingMode:         {SettingChoice, SettingScopeMember, []string{SandboxModeOff, SandboxModeBorrowed, SandboxModeAll}},
	SandboxSettingForwardEnv:   {SettingLines, SettingScopeMember, nil},
	SandboxSettingPrivateHosts: {SettingLines, SettingScopeMember, nil},
	SandboxSettingOrgHosts:     {SettingLines, SettingScopeOrg, nil},
	SandboxSettingMemberHosts:  {SettingChoice, SettingScopeOrg, []string{SandboxMemberHostsAsk, SandboxMemberHostsOff}},
}

// validateSandboxSettings holds a sandbox package's declaration of a
// contract key to its shape; a deliver is refused by validateSettings (the
// sandbox is not a process the platform hands values to: the desktop reads
// them).
func validateSandboxSettings(decl []Setting) error {
	for _, s := range decl {
		want, ok := sandboxSettingShapes[s.Key]
		if !ok {
			continue
		}
		if s.Type != want.typ || s.Scope != want.scope || (want.options != nil && !slices.Equal(s.Options, want.options)) {
			shape := want.typ + ", scope " + want.scope
			if want.options != nil {
				shape += ", options " + strings.Join(want.options, ", ")
			}
			return fmt.Errorf("settings.%s: the platform reads this sandbox setting as %s", s.Key, shape)
		}
		if err := CheckSandboxSettingValue(s.Key, s.Default); s.Default != "" && err != nil {
			return fmt.Errorf("settings.%s.default: %w", s.Key, err)
		}
	}
	return nil
}

// CheckSandboxSettingValue is the rule for a contract key's value beyond
// its type (CheckSettingValue): every forwardEnv line a ValidForwardEnv
// pattern, every host line a ValidPrivateHost. The gateway checks an
// organization's save with it, the desktop (its copy) a member's.
func CheckSandboxSettingValue(key, v string) error {
	var bad []string
	for _, line := range SplitSettingLines(v) {
		switch key {
		case SandboxSettingForwardEnv:
			if !ValidForwardEnv(line) {
				bad = append(bad, line)
			}
		case SandboxSettingPrivateHosts, SandboxSettingOrgHosts:
			if !sandboxprotocol.ValidPrivateHost(line) {
				bad = append(bad, line)
			}
		}
	}
	switch {
	case len(bad) == 0:
		return nil
	case key == SandboxSettingForwardEnv:
		return fmt.Errorf("not an environment variable name a sandboxed run may receive: %s (upper-case names like EXAMPLE_API_KEY, or a prefix like EXAMPLE_*; never BNBLOC_*, HOME, PATH, …)", strings.Join(bad, ", "))
	default:
		return fmt.Errorf("not a host the sandbox may open: %s (a name like npm.internal.example or an address like 10.0.0.5; no URL, port or wildcard, never localhost, link-local or metadata addresses)", strings.Join(bad, ", "))
	}
}

// CheckKindSettings applies a kind's own value rules to the values of one
// scope a save would store (after MergeSettings): a refusal names the
// setting. Only the sandbox kind has any.
func CheckKindSettings(kind string, decl []Setting, values map[string]string) error {
	if kind != string(extreg.KindSandbox) {
		return nil
	}
	for _, s := range decl {
		if v, ok := values[s.Key]; ok {
			if err := CheckSandboxSettingValue(s.Key, v); err != nil {
				return &SettingRefusal{Key: s.Key, Reason: s.Label + ": " + err.Error()}
			}
		}
	}
	return nil
}
