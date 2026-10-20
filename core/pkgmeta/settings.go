package pkgmeta

import (
	"fmt"
	"maps"
	"math"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Component settings (docs/prd/2026-10-19-component-settings.md): the one
// declaration of what a component needs set (a URL, a key, a model, a
// path, a switch), who sets it (its scope) and how the value reaches the
// component. This file is the one validator of a declaration and of a
// value: the gateway checks an admin's save with it, the bridge and the
// desktop (a copy pinned by testdata/setting_values.json) a member's.

// The names of a setting declaration for package authors (internal/wire
// owns their JSON).
type (
	Setting        = wire.ComponentSetting
	SettingDeliver = wire.ComponentSettingDeliver
)

// Setting types and scopes (wire owns the values, so the web's unions are
// generated from them).
const (
	SettingText    = wire.SettingText
	SettingSecret  = wire.SettingSecret
	SettingURL     = wire.SettingURL
	SettingNumber  = wire.SettingNumber
	SettingBoolean = wire.SettingBoolean
	SettingChoice  = wire.SettingChoice
	SettingPath    = wire.SettingPath

	SettingScopePlatform = wire.SettingScopePlatform
	SettingScopeOrg      = wire.SettingScopeOrg
	SettingScopeMember   = wire.SettingScopeMember
)

// ComponentSettingsFileEnv names the file holding every value of a
// component's settings (platform, organization and member values merged by
// key; secrets included): an owner-only (0600) JSON object {key: value} the
// platform writes before it starts the component's process (the bridge for
// a stdio MCP tool, the desktop for a plugin) and rewrites when a value
// changes.
const ComponentSettingsFileEnv = "BNBLOC_COMPONENT_SETTINGS_FILE"

// Declaration bounds: a handful of short, form-sized fields.
const (
	maxSettingLabel   = 80
	maxSettingHelp    = 300
	maxSettingOptions = 32
	maxSettingOption  = 80
)

var settingKeyRE = regexp.MustCompile(`^[a-z][a-zA-Z0-9]{0,39}$`)

// ValidSettingKey is the rule for a setting's key: a lower-case letter,
// then letters and digits, at most 40 (a declaration's, and the keys
// content names in requires.mcp).
func ValidSettingKey(key string) bool { return settingKeyRE.MatchString(key) }

// settingNumberRE is a number value: JSON's number syntax, so every reader
// (Go, the desktop, a component) parses it alike.
var settingNumberRE = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

var settingTypes = []string{SettingText, SettingSecret, SettingURL, SettingNumber, SettingBoolean, SettingChoice, SettingPath}

var settingScopes = []string{SettingScopePlatform, SettingScopeOrg, SettingScopeMember}

// processEnv are the variables a setting may never deliver: each changes
// how a process runs, not what a component reads. A package that set one
// would choose the program a name runs (PATH), the libraries every program
// loads (LD_*, DYLD_*), code an interpreter loads at start (NODE_OPTIONS,
// PYTHONSTARTUP, …), whose home and files it uses (HOME, TMPDIR, XDG_*), or
// which certificates and proxies every TLS and HTTP client trusts
// (SSL_CERT_FILE, HTTPS_PROXY, …). Lower-case spellings (http_proxy) are
// refused by the env name rule itself (ValidEnvName: upper case only). An
// entry ending in "*" is a prefix.
var processEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "PWD", "OLDPWD", "TMPDIR", "TMP", "TEMP",
	"IFS", "ENV", "BASH_ENV", "CDPATH", "PROMPT_COMMAND", "PS4", "XDG_*",
	"LD_*", "DYLD_*",
	"NODE_OPTIONS", "NODE_PATH", "NODE_EXTRA_CA_CERTS",
	"PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME", "PYTHONUSERBASE",
	"RUBYOPT", "RUBYLIB", "PERL5OPT", "PERL5LIB", "PERLLIB",
	"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "JDK_JAVA_OPTIONS", "CLASSPATH",
	"GOFLAGS", "GOROOT", "GOPATH", "GOTOOLCHAIN",
	"GIT_SSH", "GIT_SSH_COMMAND", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_AUTH_SOCK",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
}

// platformEnv are the names the platform itself sets in a component's
// process: never a setting's, even inside the component's own BNBLOC_
// namespace (a component named "room" or "component").
var platformEnv = []string{
	ComponentSettingsFileEnv,
	RoomRelayURLEnv, RoomRelayTokenEnv,
	"BNBLOC_PLUGIN_HOST_URL", "BNBLOC_PLUGIN_HOST_TOKEN",
}

// mcpReservedHeaders are request headers an http MCP tool's setting never
// delivers: the transport's own (the MCP client sets them) and
// Authorization, which is the bearer's (deliver.bearer or the auth block).
var mcpReservedHeaders = []string{
	"Authorization", "Host", "Connection", "Content-Type", "Content-Length", "Transfer-Encoding",
	"Accept", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-Id",
}

// reservedEnvPrefix is the platform's variables' prefix.
const reservedEnvPrefix = "BNBLOC_"

// OwnEnvPrefix is a component's own namespace under the reserved BNBLOC_
// prefix: BNBLOC_<its name upper-cased, '-' and '.' as '_'>_. A setting may
// deliver a name in it (a browser component's BNBLOC_BROWSER_CHROME); every
// other BNBLOC_ name stays the platform's.
func OwnEnvPrefix(component string) string {
	return "BNBLOC_" + strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToUpper(component)) + "_"
}

// ValidSettingEnv is the one rule for the variable a setting of component
// delivers: an env name (ValidEnvName), never one that changes how a
// process runs (processEnv), and under BNBLOC_ only in the component's own
// namespace (OwnEnvPrefix) and never a name the platform sets.
func ValidSettingEnv(component, name string) error {
	if !ValidEnvName(name) {
		return fmt.Errorf("%q must be an upper-case identifier", name)
	}
	for _, p := range processEnv {
		if prefix, ok := strings.CutSuffix(p, "*"); (ok && strings.HasPrefix(name, prefix)) || name == p {
			return fmt.Errorf("%q changes how a process runs; a setting may not set it", name)
		}
	}
	if strings.HasPrefix(name, reservedEnvPrefix) {
		if slices.Contains(platformEnv, name) {
			return fmt.Errorf("%q is set by the platform", name)
		}
		if own := OwnEnvPrefix(component); !strings.HasPrefix(name, own) || len(name) == len(own) {
			return fmt.Errorf("%q: the %s prefix is reserved (a component's own variables are %s…)", name, reservedEnvPrefix, own)
		}
	}
	return nil
}

// validateSettings checks a package's settings (validatePackage runs it
// for every kind; the field-to-kinds table says which kinds may declare
// any). A value is delivered as an env var only to a process the platform
// starts (a plugin sidecar, a stdio MCP tool) and as a header only to an
// http MCP tool.
func validateSettings(pkg Package) error {
	if len(pkg.Settings) > wire.MaxComponentSettings {
		return fmt.Errorf("settings: at most %d", wire.MaxComponentSettings)
	}
	process := pkg.Kind == KindPluginPackageV1 || (pkg.Kind == KindMCPPackageV1 && pkg.Runtime == RuntimeStdio)
	httpTool := pkg.Kind == KindMCPPackageV1 && pkg.Runtime == RuntimeEndpoint
	ownEnv := map[string]bool{}
	if sc := pkg.Sidecar; sc != nil {
		for _, name := range []string{sc.ListenEnv, sc.SupervisedEnv, sc.TokenEnv, sc.DataDirEnv, sc.MountsEnv, sc.MountRootEnv} {
			ownEnv[name] = name != ""
		}
		for _, f := range sc.Files {
			ownEnv[f.Env] = true
		}
	}
	keys, envs, headers := map[string]bool{}, map[string]bool{}, map[string]bool{}
	bearers := 0
	for i, s := range pkg.Settings {
		at := fmt.Sprintf("settings[%d]", i)
		if !ValidSettingKey(s.Key) {
			return fmt.Errorf("%s.key %q: a lower-case letter, then letters and digits (at most 40)", at, s.Key)
		}
		if keys[s.Key] {
			return fmt.Errorf("settings: duplicate key %q", s.Key)
		}
		keys[s.Key] = true
		at = "settings." + s.Key
		if l := utf8.RuneCountInString(strings.TrimSpace(s.Label)); l == 0 || l > maxSettingLabel || strings.ContainsAny(s.Label, "\r\n") {
			return fmt.Errorf("%s.label must be one line of 1-%d characters", at, maxSettingLabel)
		}
		if utf8.RuneCountInString(s.Help) > maxSettingHelp || !utf8.ValidString(s.Help) {
			return fmt.Errorf("%s.help must be text of at most %d characters", at, maxSettingHelp)
		}
		if !slices.Contains(settingTypes, s.Type) {
			return fmt.Errorf("%s.type must be one of %s, got %q", at, strings.Join(settingTypes, ", "), s.Type)
		}
		if !slices.Contains(settingScopes, s.Scope) {
			return fmt.Errorf("%s.scope must be one of %s, got %q", at, strings.Join(settingScopes, ", "), s.Scope)
		}
		if s.Type == SettingPath && s.Scope != SettingScopeMember {
			return fmt.Errorf("%s: a path is on the member's own machine (scope %q only)", at, SettingScopeMember)
		}
		if err := validateSettingOptions(s); err != nil {
			return fmt.Errorf("%s: %w", at, err)
		}
		if s.Default != "" {
			if s.Type == SettingSecret {
				return fmt.Errorf("%s: a secret has no default", at)
			}
			if err := CheckSettingValue(s, s.Default); err != nil {
				return fmt.Errorf("%s.default: %w", at, err)
			}
		}
		d := s.Deliver
		if d == nil {
			continue
		}
		if d.Env == "" && d.Header == "" && !d.Bearer {
			return fmt.Errorf("%s.deliver names no env, header or bearer", at)
		}
		if d.Env != "" {
			if !process {
				return fmt.Errorf("%s.deliver.env: only a plugin or a stdio MCP tool runs as a process here", at)
			}
			if err := ValidSettingEnv(pkg.Name, d.Env); err != nil {
				return fmt.Errorf("%s.deliver.env: %w", at, err)
			}
			if envs[d.Env] || ownEnv[d.Env] {
				return fmt.Errorf("%s.deliver.env: %q is delivered twice", at, d.Env)
			}
			envs[d.Env] = true
		}
		if (d.Header != "" || d.Bearer) && !httpTool {
			return fmt.Errorf("%s.deliver: a header or bearer is only for an http MCP tool", at)
		}
		if d.Header != "" && d.Bearer {
			return fmt.Errorf("%s.deliver: a header or a bearer, not both", at)
		}
		if d.Header != "" {
			if err := validMCPHeader(d.Header); err != nil {
				return fmt.Errorf("%s.deliver.header: %w", at, err)
			}
			if headers[strings.ToLower(d.Header)] {
				return fmt.Errorf("%s.deliver.header: %q is delivered twice", at, d.Header)
			}
			headers[strings.ToLower(d.Header)] = true
		}
		if d.Bearer {
			bearers++
		}
	}
	if bearers > 1 {
		return fmt.Errorf("settings: at most one setting is delivered as the bearer")
	}
	return nil
}

func validateSettingOptions(s Setting) error {
	if s.Type != SettingChoice {
		if len(s.Options) > 0 {
			return fmt.Errorf("options are for a choice")
		}
		return nil
	}
	if len(s.Options) == 0 || len(s.Options) > maxSettingOptions {
		return fmt.Errorf("a choice needs 1-%d options", maxSettingOptions)
	}
	seen := map[string]bool{}
	for _, o := range s.Options {
		if l := utf8.RuneCountInString(o); l == 0 || l > maxSettingOption || strings.TrimSpace(o) != o || strings.ContainsAny(o, "\r\n") {
			return fmt.Errorf("option %q must be one trimmed line of 1-%d characters", o, maxSettingOption)
		}
		if seen[o] {
			return fmt.Errorf("duplicate option %q", o)
		}
		seen[o] = true
	}
	return nil
}

// validMCPHeader is the rule for a request header an http MCP tool gets a
// value in: a header name the MCP client does not set itself.
func validMCPHeader(name string) error {
	if !mcpHeaderRE.MatchString(name) {
		return fmt.Errorf("%q must be a header name", name)
	}
	for _, h := range mcpReservedHeaders {
		if strings.EqualFold(h, name) {
			return fmt.Errorf("%q is set by the MCP client (a bearer is deliver.bearer)", name)
		}
	}
	return nil
}

// CheckSettingValue is the one rule for a value of setting s (trimmed,
// non-empty): text of at most MaxComponentSettingValueBytes, and for a url
// an absolute http(s) URL, a number in decimal, a boolean "true" or
// "false", a choice one of its options.
func CheckSettingValue(s Setting, v string) error {
	if len(v) > wire.MaxComponentSettingValueBytes || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
		return fmt.Errorf("must be text of at most %d bytes", wire.MaxComponentSettingValueBytes)
	}
	switch s.Type {
	case SettingURL:
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(v, " \t\r\n") {
			return fmt.Errorf("must be an http(s) URL, such as https://api.example.com")
		}
	case SettingNumber:
		if f, err := strconv.ParseFloat(v, 64); err != nil || !settingNumberRE.MatchString(v) || math.IsInf(f, 0) {
			return fmt.Errorf("must be a number, such as 42 or 0.5")
		}
	case SettingBoolean:
		if v != "true" && v != "false" {
			return fmt.Errorf(`must be "true" or "false"`)
		}
	case SettingChoice:
		if !slices.Contains(s.Options, v) {
			return fmt.Errorf("must be one of %s", strings.Join(s.Options, ", "))
		}
	}
	return nil
}

// SettingRefusal is why a save of settings was refused: the setting it is
// about (Key) and the reason, worded with the setting's label (Error). A
// form or a component's settings page shows it at that field.
type SettingRefusal struct {
	Key    string
	Reason string
}

func (e *SettingRefusal) Error() string { return e.Reason }

// MergeSettings applies an update to the stored values of one scope of a
// declaration: a key absent from update is left as it is, an empty value
// clears it (an empty secret keeps the stored one), a key in clear is
// cleared (a secret too). Every key named must be a setting of that scope,
// every value valid for its type (CheckSettingValue), and every required
// setting of the scope set in the end (a default counts). Values of keys
// the declaration no longer has in this scope are dropped. It returns the
// values to store and the keys whose stored value changed (for the audit:
// names, never values); a refusal is a *SettingRefusal naming the first
// setting refused.
func MergeSettings(decl []Setting, scope string, current, update map[string]string, clear []string) (map[string]string, []string, error) {
	inScope := map[string]Setting{}
	for _, s := range decl {
		if s.Scope == scope {
			inScope[s.Key] = s
		}
	}
	named := append(slices.Sorted(maps.Keys(update)), clear...)
	for _, k := range named {
		if _, ok := inScope[k]; !ok {
			return nil, nil, &SettingRefusal{Key: k, Reason: fmt.Sprintf("%q is not a %s setting of this component", k, scope)}
		}
	}
	next := map[string]string{}
	for _, s := range decl {
		if s.Scope != scope {
			continue
		}
		v, given := update[s.Key]
		v = strings.TrimSpace(v)
		switch {
		case slices.Contains(clear, s.Key):
			v = ""
		case !given || (v == "" && s.Type == SettingSecret):
			v = strings.TrimSpace(current[s.Key])
		case v != "":
			if err := CheckSettingValue(s, v); err != nil {
				return nil, nil, &SettingRefusal{Key: s.Key, Reason: s.Label + " " + err.Error()}
			}
		}
		if v != "" {
			next[s.Key] = v
		}
	}
	if unset := UnsetSettings(decl, next, scope); len(unset) > 0 {
		return nil, nil, &SettingRefusal{Key: unset[0], Reason: inScope[unset[0]].Label + " is required"}
	}
	var changed []string
	for k := range current {
		if current[k] != next[k] {
			changed = append(changed, k)
		}
	}
	for k := range next {
		if _, had := current[k]; !had {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	return next, changed, nil
}

// EffectiveSettings is what a component gets: each declared setting's
// value (trimmed), else its default; a setting with neither is left out,
// and so is any key the declaration lacks.
func EffectiveSettings(decl []Setting, values map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range decl {
		v := strings.TrimSpace(values[s.Key])
		if v == "" {
			v = s.Default
		}
		if v != "" {
			out[s.Key] = v
		}
	}
	return out
}

// UnsetSettings is the one "needs setup" rule: the keys, in declaration
// order, of the required settings (of the scopes given; none = every
// scope) without an effective value (EffectiveSettings). A component with
// any does not run and is not offered to agents.
func UnsetSettings(decl []Setting, values map[string]string, scopes ...string) []string {
	eff := EffectiveSettings(decl, values)
	var out []string
	for _, s := range decl {
		if s.Required && eff[s.Key] == "" && (len(scopes) == 0 || slices.Contains(scopes, s.Scope)) {
			out = append(out, s.Key)
		}
	}
	return out
}

// SettingsOfScope is the part of a declaration a scope's owner sets.
func SettingsOfScope(decl []Setting, scopes ...string) []Setting {
	var out []Setting
	for _, s := range decl {
		if slices.Contains(scopes, s.Scope) {
			out = append(out, s)
		}
	}
	return out
}

// NameValue is one environment variable or request header.
type NameValue struct {
	Name  string
	Value string
}

// SettingsEnv is the environment a process component gets from its
// effective values (EffectiveSettings): each deliver.env with a value.
func SettingsEnv(decl []Setting, effective map[string]string) []NameValue {
	var out []NameValue
	for _, s := range decl {
		if s.Deliver != nil && s.Deliver.Env != "" && effective[s.Key] != "" {
			out = append(out, NameValue{Name: s.Deliver.Env, Value: effective[s.Key]})
		}
	}
	return out
}
