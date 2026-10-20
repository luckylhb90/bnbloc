package wire

// Component settings (docs/prd/2026-10-19-component-settings.md): the
// values of the settings a component declares (ComponentSetting), set by
// the super admin (platform scope), an organization admin (org scope) or
// each member on their desktop (member scope).

// ComponentSettingField is one declared setting with its state in the scope
// a view shows: a secret's value is never returned (Set says whether one is
// stored).
type ComponentSettingField struct {
	ComponentSetting
	Set   bool   `json:"set"`
	Value string `json:"value,omitempty"`
}

// ComponentSettingsView answers GET and PUT of
// /v1/platform/components/{name}/settings (platform scope) and
// /v1/tenant/components/{name}/settings (the caller's organization): the
// component's settings of that scope with their state, and the required
// settings of this and the scopes above it that are not set (the component
// needs setup while any is).
type ComponentSettingsView struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope" tstype:"ComponentSettingScope"`
	// Available is false when the gateway has no secret store key: values
	// cannot be stored.
	Available bool `json:"available"`
	// ReachesMembers: the component runs on members' computers (a plugin,
	// a stdio MCP tool), so these values are sent to them.
	ReachesMembers bool `json:"reachesMembers"`
	// Asks: an endpoint component beside the gateway (a notifier, an auth
	// provider) that reads these values itself (GET /v1/component-settings);
	// a change reaches it on its next ask.
	Asks        bool                    `json:"asks"`
	Fields      []ComponentSettingField `json:"fields"`
	Missing     []ComponentSetting      `json:"missing"`
	UpdatedBy   string                  `json:"updatedBy,omitempty"`
	UpdatedAtMs int64                   `json:"updatedAtMs,omitempty"`
	// Page is the component's own settings page for this scope, when it
	// declares one: it is shown instead of the one form.
	Page *ComponentSettingsPageFrame `json:"page,omitempty"`
}

// ComponentSettingsPageFrame is where a component's own settings page
// (ComponentSettingsPage) loads for one scope: the browser's frame base on
// the gateway (a ticket for the caller, this scope and this version
// included; the host appends Entry), and the bundle's pin, by which the
// desktop shell serves the same files itself.
type ComponentSettingsPageFrame struct {
	Entry            string `json:"entry"`
	BundleSHA256     string `json:"bundleSha256"`
	FrameURL         string `json:"frameUrl"`
	FrameExpiresAtMs int64  `json:"frameExpiresAtMs"`
}

// ComponentSettingsUpdate is a PUT of a scope's values: a key absent is
// left as it is, an empty value clears it (an empty secret keeps the
// stored one), Clear clears keys (a secret too). Values of settings the
// component no longer declares are dropped.
type ComponentSettingsUpdate struct {
	Values map[string]string `json:"values"`
	Clear  []string          `json:"clear,omitempty"`
}

// ComponentSettingsResolved answers GET
// /v1/components/{name}/settings/resolved: the platform and organization
// values of a component installed for the caller, secrets included, which
// the caller's desktop hands the component when it launches it there. Missing
// are the required settings of those scopes that are not set.
type ComponentSettingsResolved struct {
	Values  map[string]string `json:"values"`
	Missing []string          `json:"missing"`
}

// ComponentSettingsPath is where an endpoint component beside the gateway
// (a notifier, an auth provider) asks for its settings: a GET signed with
// its component credential, X-Bnbloc-Component "<kind>/<name>" and
// X-Bnbloc-Signature over "GET /v1/component-settings"
// (docs/architecture/endpoint-components.md).
const ComponentSettingsPath = "/v1/component-settings"

// EndpointComponentSettings answers GET /v1/component-settings: the stored
// platform values of the calling component (secrets included; a setting
// with no stored value is absent and the component applies its own
// default), each organization's values for the organizations the install
// serves that have stored any, the required settings not set per scope,
// and Version, a hash of the rest: the component applies an answer only
// when it changed.
type EndpointComponentSettings struct {
	Kind     string                       `json:"kind"`
	Name     string                       `json:"name"`
	Version  string                       `json:"version"`
	Platform map[string]string            `json:"platform"`
	Orgs     map[string]map[string]string `json:"orgs"`
	Missing  EndpointSettingsMissing      `json:"missing"`
}

// EndpointSettingsMissing are the required settings without a value: the
// platform's, and each listed organization's own.
type EndpointSettingsMissing struct {
	Platform []string            `json:"platform"`
	Orgs     map[string][]string `json:"orgs"`
}

// OrgSettingsComponent is one component whose organization settings the
// caller's organization sets (GET /v1/tenant/component-settings), of any
// kind: a member's catalog lists only what runs for members, while an
// endpoint component beside the gateway takes organization settings too.
type OrgSettingsComponent struct {
	Kind            string                 `json:"kind"`
	Name            string                 `json:"name"`
	Version         string                 `json:"version"`
	Description     string                 `json:"description,omitempty"`
	Settings        []ComponentSetting     `json:"settings"`
	SettingsPage    *ComponentSettingsPage `json:"settingsPage,omitempty"`
	SettingsMissing []string               `json:"settingsMissing"`
}

// OrgSettingsComponentsReply answers GET /v1/tenant/component-settings.
type OrgSettingsComponentsReply struct {
	Components []OrgSettingsComponent `json:"components"`
}
