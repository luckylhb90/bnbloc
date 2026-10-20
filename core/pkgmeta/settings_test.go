package pkgmeta

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// pluginWithSettings is the plugin fixture declaring settings.
func pluginWithSettings(t *testing.T, settings ...map[string]any) []byte {
	t.Helper()
	return validPluginPackageBody(t, func(b map[string]any) { b["settings"] = settings })
}

func TestSettingsDeclarationRefusals(t *testing.T) {
	many := []map[string]any{}
	for i := range wire.MaxComponentSettings + 1 {
		many = append(many, map[string]any{"key": fmt.Sprintf("k%d", i), "label": "L", "type": "text", "scope": "member"})
	}
	text := func(extra map[string]any) map[string]any {
		s := map[string]any{"key": "apiBase", "label": "API base", "type": "text", "scope": "org"}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}
	cases := map[string]struct {
		settings []map[string]any
		want     string
	}{
		"too many":             {many, "at most 24"},
		"key shape":            {[]map[string]any{text(map[string]any{"key": "Api"})}, "a lower-case letter"},
		"key with a dash":      {[]map[string]any{text(map[string]any{"key": "api-base"})}, "a lower-case letter"},
		"key too long":         {[]map[string]any{text(map[string]any{"key": "a" + strings.Repeat("b", 40)})}, "a lower-case letter"},
		"duplicate key":        {[]map[string]any{text(nil), text(nil)}, "duplicate key"},
		"no label":             {[]map[string]any{text(map[string]any{"label": " "})}, "label must be"},
		"unknown type":         {[]map[string]any{text(map[string]any{"type": "color"})}, "type must be one of"},
		"unknown scope":        {[]map[string]any{text(map[string]any{"scope": "room"})}, "scope must be one of"},
		"path not member":      {[]map[string]any{text(map[string]any{"type": "path"})}, "a path is on the member's own machine"},
		"choice no options":    {[]map[string]any{text(map[string]any{"type": "choice"})}, "a choice needs 1-32 options"},
		"options on text":      {[]map[string]any{text(map[string]any{"options": []string{"a"}})}, "options are for a choice"},
		"duplicate option":     {[]map[string]any{text(map[string]any{"type": "choice", "options": []string{"a", "a"}})}, "duplicate option"},
		"secret default":       {[]map[string]any{text(map[string]any{"type": "secret", "default": "x"})}, "a secret has no default"},
		"bad default":          {[]map[string]any{text(map[string]any{"type": "number", "default": "many"})}, "default: must be a number"},
		"default not a choice": {[]map[string]any{text(map[string]any{"type": "choice", "options": []string{"a"}, "default": "b"})}, "must be one of a"},
		"empty deliver":        {[]map[string]any{text(map[string]any{"deliver": map[string]any{}})}, "names no env, header or bearer"},
		"header on a plugin":   {[]map[string]any{text(map[string]any{"deliver": map[string]any{"header": "X-Key"}})}, "only for an http MCP tool"},
		"bearer on a plugin":   {[]map[string]any{text(map[string]any{"deliver": map[string]any{"bearer": true}})}, "only for an http MCP tool"},
		"env twice": {[]map[string]any{
			text(map[string]any{"deliver": map[string]any{"env": "API_BASE"}}),
			text(map[string]any{"key": "other", "deliver": map[string]any{"env": "API_BASE"}}),
		}, "delivered twice"},
		"env is the sidecar's": {[]map[string]any{text(map[string]any{"deliver": map[string]any{"env": "AUDITOR_LISTEN_URL"}})}, "delivered twice"},
		"process env":          {[]map[string]any{text(map[string]any{"deliver": map[string]any{"env": "LD_PRELOAD"}})}, "changes how a process runs"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePackage(pluginWithSettings(t, c.settings...)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	// Every type and scope, a default each may take, deliveries by env.
	ok := []map[string]any{
		{"key": "apiBase", "label": "API base", "type": "url", "scope": "platform", "required": true, "default": "https://api.example.com", "deliver": map[string]any{"env": "ACME_API_BASE"}},
		{"key": "apiKey", "label": "API key", "type": "secret", "scope": "org", "required": true, "deliver": map[string]any{"env": "ACME_API_KEY"}},
		{"key": "pageSize", "label": "Page size", "type": "number", "scope": "org", "default": "20"},
		{"key": "verbose", "label": "Verbose", "type": "boolean", "scope": "member", "default": "false"},
		{"key": "quality", "label": "Quality", "type": "choice", "scope": "member", "options": []string{"low", "high"}, "default": "low"},
		{"key": "chrome", "label": "Chrome binary", "type": "path", "scope": "member", "deliver": map[string]any{"env": "BNBLOC_ACME_AUDITOR_CHROME"}},
		{"key": "note", "label": "Note", "type": "text", "scope": "member", "help": "Anything."},
	}
	if _, err := ParsePackage(pluginWithSettings(t, ok...)); err != nil {
		t.Fatalf("a full declaration: %v", err)
	}
}

// A setting delivers an env var of its own: never one that changes how a
// process runs, and under BNBLOC_ only in the component's own namespace.
func TestSettingEnvRule(t *testing.T) {
	if got := OwnEnvPrefix("local-skills.v2"); got != "BNBLOC_LOCAL_SKILLS_V2_" {
		t.Fatalf("OwnEnvPrefix = %q", got)
	}
	for name, want := range map[string]string{
		"BROWSER_CHROME":                 "",
		"BNBLOC_BROWSER_CHROME":          "",
		"BNBLOC_BROWSER_PROFILE_DIR":     "",
		"BNBLOC_BROWSER_":                "reserved",
		"BNBLOC_MEDIA_TOOLS_KEY":         "reserved", // another component's namespace
		"BNBLOC_TOKEN":                   "reserved",
		"BNBLOC_COMPONENT_SETTINGS_FILE": "set by the platform",
		"PATH":                           "changes how a process runs",
		"LD_PRELOAD":                     "changes how a process runs",
		"DYLD_INSERT_LIBRARIES":          "changes how a process runs",
		"NODE_OPTIONS":                   "changes how a process runs",
		"HTTPS_PROXY":                    "changes how a process runs",
		"XDG_CONFIG_HOME":                "changes how a process runs",
		"https_proxy":                    "upper-case identifier",
		"9LIVES":                         "upper-case identifier",
	} {
		err := ValidSettingEnv("browser", name)
		if (want == "") != (err == nil) || (want != "" && !strings.Contains(err.Error(), want)) {
			t.Errorf("ValidSettingEnv(browser, %s) = %v, want %q", name, err, want)
		}
	}
	// Inside its own namespace, a name the platform sets stays the platform's.
	for _, c := range [][2]string{{"room", RoomRelayTokenEnv}, {"component", ComponentSettingsFileEnv}, {"plugin", "BNBLOC_PLUGIN_HOST_TOKEN"}} {
		if err := ValidSettingEnv(c[0], c[1]); err == nil || !strings.Contains(err.Error(), "set by the platform") {
			t.Errorf("ValidSettingEnv(%s, %s) = %v", c[0], c[1], err)
		}
	}
}

// settingFixture is testdata/setting_values.json, shared with the desktop.
type settingFixture struct {
	Values []struct {
		Setting Setting `json:"setting"`
		Value   string  `json:"value"`
		OK      bool    `json:"ok"`
	} `json:"values"`
	Merges []struct {
		Settings []Setting         `json:"settings"`
		Scope    string            `json:"scope"`
		Current  map[string]string `json:"current"`
		Update   map[string]string `json:"update"`
		Clear    []string          `json:"clear"`
		Next     map[string]string `json:"next"`
		Changed  []string          `json:"changed"`
		Error    string            `json:"error"`
		Refused  string            `json:"refused"`
	} `json:"merges"`
	Effective []struct {
		Settings  []Setting         `json:"settings"`
		Values    map[string]string `json:"values"`
		Effective map[string]string `json:"effective"`
		Unset     []string          `json:"unset"`
		Env       [][2]string       `json:"env"`
	} `json:"effective"`
}

func TestSettingValueRules(t *testing.T) {
	raw, err := os.ReadFile("testdata/setting_values.json")
	if err != nil {
		t.Fatal(err)
	}
	var f settingFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Values {
		if err := CheckSettingValue(c.Setting, c.Value); (err == nil) != c.OK {
			t.Errorf("CheckSettingValue(%s, %q) = %v, want ok=%v", c.Setting.Type, c.Value, err, c.OK)
		}
	}
	for i, c := range f.Merges {
		next, changed, err := MergeSettings(c.Settings, c.Scope, c.Current, c.Update, c.Clear)
		if c.Error != "" {
			var refusal *SettingRefusal
			if !errors.As(err, &refusal) || !strings.Contains(err.Error(), c.Error) || refusal.Key != c.Refused {
				t.Errorf("merge %d: err = %#v, want %q about %q", i, err, c.Error, c.Refused)
			}
			continue
		}
		if err != nil || fmt.Sprint(next) != fmt.Sprint(c.Next) || !slices.Equal(changed, c.Changed) {
			t.Errorf("merge %d = %v %v %v, want %v %v", i, next, changed, err, c.Next, c.Changed)
		}
	}
	for i, c := range f.Effective {
		eff := EffectiveSettings(c.Settings, c.Values)
		if fmt.Sprint(eff) != fmt.Sprint(c.Effective) {
			t.Errorf("effective %d = %v, want %v", i, eff, c.Effective)
		}
		if got := UnsetSettings(c.Settings, c.Values); !slices.Equal(got, c.Unset) {
			t.Errorf("unset %d = %v, want %v", i, got, c.Unset)
		}
		var env [][2]string
		for _, nv := range SettingsEnv(c.Settings, eff) {
			env = append(env, [2]string{nv.Name, nv.Value})
		}
		if !slices.Equal(env, c.Env) {
			t.Errorf("env %d = %v, want %v", i, env, c.Env)
		}
	}
	// Needs setup narrowed to the scopes a reader knows.
	decl := f.Effective[0].Settings
	if got := UnsetSettings(decl, f.Effective[0].Values, SettingScopePlatform, SettingScopeOrg); !slices.Equal(got, []string{"apiKey"}) {
		t.Errorf("unset of platform and org = %v", got)
	}
}
