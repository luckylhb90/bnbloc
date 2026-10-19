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
// /v1/org/components/{name}/settings (the caller's organization): the
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
	ReachesMembers bool                    `json:"reachesMembers"`
	Fields         []ComponentSettingField `json:"fields"`
	Missing        []ComponentSetting      `json:"missing"`
	UpdatedBy      string                  `json:"updatedBy,omitempty"`
	UpdatedAtMs    int64                   `json:"updatedAtMs,omitempty"`
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
