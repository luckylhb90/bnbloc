package pkgmeta

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// KindPluginPackageV1 is the package-envelope kind for an out-of-process plugin.
// A plugin subscribes to code-owned event hooks and runs as a supervised
// sidecar; it is never driven in-process and is never a core.
const KindPluginPackageV1 = "bnbloc.plugin.package/v1"

const KindAuthProviderPackageV1 = "bnbloc.auth-provider.package/v1"

// KindNotifierPackageV1 is the package-envelope kind for a notifier: an
// externally supervised endpoint (runtime "endpoint") that receives signed
// member notifications from realtime (docs/notifications.md).
const KindNotifierPackageV1 = "bnbloc.notifier.package/v1"

// PluginContractV1 is the only typed plugin wire-contract version. The sidecar
// receives a versioned delivery envelope ({event, version, tenantId, roomId,
// payload}) at POST /events; the contract version pins that envelope shape so a
// future envelope change is a new contract, not a silent reinterpretation.
const PluginContractV1 = "bnbloc.plugin.contract/v1"

const AuthProviderContractV1 = "bnbloc.auth-provider.contract/v1"

// NotifierContractV1 pins the notifier wire contract: GET {baseUrl}/discover
// answers {name, contract}, POST {baseUrl}/v1/notifications receives one
// signed notification payload.
const NotifierContractV1 = "bnbloc.notifier.contract/v1"

// KindPlugin is the plugin's extreg kind. Hooks dispatch in the realtime host,
// so the plugin kind activates there (Task 4 owns the installer; this descriptor
// only makes the kind known to the registry).
const KindPlugin extreg.Kind = "plugin"

// HookSpec is the versioned delivery contract for one event hook: which room
// event (Event) and which payload version (Version) a subscribing sidecar
// receives. Both travel in every delivery envelope so a plugin never has to
// guess the payload shape.
type HookSpec struct {
	Event   string
	Version string
}

// hookRegistry is the fixed, code-owned set of event hooks a plugin may declare.
// It is deliberately small and concrete: a frontend string list can never add a
// hook here, and a plugin declaring any key absent from this map is rejected
// before persistence. The keys are "<event>.v<version>".
var hookRegistry = map[string]HookSpec{
	"chat_message.v1":   {Event: "chat_message", Version: "1"},
	"agent_messages.v1": {Event: "agent_messages", Version: "1"},
	// notification.v1 is not a room event: it carries one member's
	// notification (wire.PluginNotification) to that member's own desktop
	// plugin, which delivers it in their place (docs/notifications.md).
	NotificationHook: {Event: NotificationHookEvent, Version: "1"},
}

// NotificationHook is the hook id of member notifications, and
// NotificationHookEvent its envelope event name. No room event has this kind,
// so the room-event tap never matches it.
const (
	NotificationHook      = "notification.v1"
	NotificationHookEvent = "notification"
)

// PluginHostChat is the plugin host API capability to post chat as the
// member (POST {BNBLOC_PLUGIN_HOST_URL}/v1/chat on the member's desktop).
const PluginHostChat = "chat"

// pluginHostPermissions is the code-owned set of host API capabilities a
// plugin may declare in permissions.host.
var pluginHostPermissions = map[string]bool{PluginHostChat: true}

var pluginCapabilities = map[string]bool{
	"room-events": true,
}

// LookupHook resolves a declared hook id to its delivery spec.
func LookupHook(id string) (HookSpec, bool) {
	spec, ok := hookRegistry[id]
	return spec, ok
}

// RegisteredHooks lists every known hook id (sorted) for admin/UI validation.
func RegisteredHooks() []string {
	out := make([]string, 0, len(hookRegistry))
	for id := range hookRegistry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func validatePluginPackage(pkg Package) error {
	if err := validatePluginCapabilities(pkg.Capabilities); err != nil {
		return err
	}
	if pkg.Contract != PluginContractV1 {
		return fmt.Errorf("unknown plugin contract %q", pkg.Contract)
	}
	if pkg.Runtime != RuntimeSidecar {
		return fmt.Errorf(`plugin runtime must be "sidecar"`)
	}
	if len(pkg.Hooks) == 0 {
		return fmt.Errorf("plugin must declare at least one hook")
	}
	for _, id := range pkg.Hooks {
		if _, ok := hookRegistry[id]; !ok {
			return fmt.Errorf("unknown hook %q", id)
		}
	}
	if len(pkg.Artifacts) == 0 {
		return fmt.Errorf("plugin sidecar requires artifacts")
	}
	if pkg.Sidecar == nil || strings.TrimSpace(pkg.Sidecar.LocalURL) == "" {
		return fmt.Errorf("plugin sidecar requires sidecar.localUrl")
	}
	if err := ValidateLoopbackHTTPURL(pkg.Sidecar.LocalURL); err != nil {
		return fmt.Errorf("sidecar.localUrl must be a loopback http URL")
	}
	seen := map[string]bool{}
	for _, p := range pkg.Permissions.Host {
		if !pluginHostPermissions[p] {
			return fmt.Errorf("unknown permissions.host entry %q", p)
		}
		if seen[p] {
			return fmt.Errorf("duplicate permissions.host entry %q", p)
		}
		seen[p] = true
	}
	return validateLocalSettings(pkg.LocalSettings)
}

func validateAuthProviderPackage(pkg Package) error {
	return validateEndpointPackage(pkg, "auth-provider", AuthProviderContractV1)
}

func validateNotifierPackage(pkg Package) error {
	return validateEndpointPackage(pkg, "notifier", NotifierContractV1)
}

// Local settings schema bounds: a handful of short, form-sized fields.
const (
	maxLocalSettings      = 10
	maxLocalSettingLabel  = 80
	maxLocalSettingHelp   = 300
	maxLocalSettingKeyLen = 40
)

var localSettingKeyRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// validateLocalSettings checks a plugin's desktop-local settings schema: at
// most maxLocalSettings fields, unique identifier-shaped keys, and non-empty
// bounded labels.
func validateLocalSettings(fields []LocalSetting) error {
	if len(fields) > maxLocalSettings {
		return fmt.Errorf("localSettings: at most %d fields", maxLocalSettings)
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if len(f.Key) > maxLocalSettingKeyLen || !localSettingKeyRE.MatchString(f.Key) {
			return fmt.Errorf("localSettings: key %q must be an identifier of at most %d characters", f.Key, maxLocalSettingKeyLen)
		}
		if seen[f.Key] {
			return fmt.Errorf("localSettings: duplicate key %q", f.Key)
		}
		seen[f.Key] = true
		if l := len([]rune(strings.TrimSpace(f.Label))); l == 0 || l > maxLocalSettingLabel {
			return fmt.Errorf("localSettings: %s label must be 1-%d characters", f.Key, maxLocalSettingLabel)
		}
		if len([]rune(f.Help)) > maxLocalSettingHelp {
			return fmt.Errorf("localSettings: %s help must be at most %d characters", f.Key, maxLocalSettingHelp)
		}
	}
	return nil
}

// validateEndpointPackage is the one shape rule of every endpoint component
// kind (auth-provider, notifier): the kind's contract, runtime "endpoint", no
// artifacts (nothing is downloaded or supervised by bnbloc), and an absolute
// endpoint.baseUrl. label names the kind in errors.
func validateEndpointPackage(pkg Package, label, contract string) error {
	if pkg.Contract != contract {
		return fmt.Errorf("unknown %s contract %q", label, pkg.Contract)
	}
	if pkg.Runtime != RuntimeEndpoint {
		return fmt.Errorf(`%s runtime must be "endpoint"`, label)
	}
	if len(pkg.Artifacts) > 0 {
		return fmt.Errorf(`%s runtime "endpoint" must not declare artifacts`, label)
	}
	if pkg.Endpoint == nil || strings.TrimSpace(pkg.Endpoint.BaseURL) == "" {
		return fmt.Errorf("%s endpoint requires endpoint.baseUrl", label)
	}
	if err := validateAbsoluteHTTPURL(pkg.Endpoint.BaseURL); err != nil {
		return fmt.Errorf("endpoint.baseUrl must be an absolute http(s) URL")
	}
	return nil
}

func validatePluginCapabilities(caps []string) error {
	for _, cap := range caps {
		if !pluginCapabilities[cap] {
			return fmt.Errorf("unknown plugin capability %q", cap)
		}
	}
	return nil
}

func init() {
	RegisterKindValidator(KindPluginPackageV1, validatePluginPackage)
	RegisterKindValidator(KindAuthProviderPackageV1, validateAuthProviderPackage)
	RegisterKindValidator(KindNotifierPackageV1, validateNotifierPackage)
	extreg.RegisterKind(extreg.KindDescriptor{
		Kind:  KindPlugin,
		Hosts: []extreg.Service{extreg.ServiceRealtime},
	})
}
