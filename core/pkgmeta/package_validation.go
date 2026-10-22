package pkgmeta

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/wire"
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

// KindShareTargetPackageV1 is the package-envelope kind for a share target:
// an externally supervised endpoint (runtime "endpoint") that takes one
// shared item, as a card, to a conversation of another app
// (docs/architecture/share-targets.md).
const KindShareTargetPackageV1 = "bnbloc.share-target.package/v1"

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

// ShareTargetContractV1 pins the share-target wire contract: GET
// {baseUrl}/discover answers {name, contract}, POST {baseUrl}/v1/share
// receives one signed share, GET {baseUrl}/v1/share/frame answers the
// client half's document.
const ShareTargetContractV1 = "bnbloc.share-target.contract/v1"

// NotifierCapabilityGroups: the notifier delivers a notification addressed
// to a named group (NotificationPayload.Group) as well as to members; the
// group names it knows are its own configuration.
const NotifierCapabilityGroups = "groups"

// notifierCapabilities is the code-owned set a notifier package may declare.
var notifierCapabilities = map[string]bool{NotifierCapabilityGroups: true}

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
	"chat_message.v1":   {Event: wire.KindChatMessage, Version: "1"},
	"agent_messages.v1": {Event: wire.KindAgentMessages, Version: "1"},
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

// Plugin capabilities: what a plugin is for. A plugin with none of the two
// below must subscribe to a hook; these give it another job and the desktop
// the matching launch and call rights.
const (
	// PluginCapabilitySkillsProvider: the plugin serves bnbloc.skills.provider/v1
	// on sidecar.localUrl behind its tokenEnv bearer; the desktop forwards the
	// host Settings → Skills view's calls to it.
	PluginCapabilitySkillsProvider = "skills-provider"
	// PluginCapabilitySandboxMounts: the plugin writes a bnbloc.sandbox.mounts/v1
	// descriptor to the file in sidecar.mountsEnv, for the sandbox bridge.
	PluginCapabilitySandboxMounts = "sandbox-mounts"
)

var pluginCapabilities = map[string]bool{
	"room-events":                  true,
	PluginCapabilitySkillsProvider: true,
	PluginCapabilitySandboxMounts:  true,
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
	if len(pkg.Hooks) == 0 && !slices.Contains(pkg.Capabilities, PluginCapabilitySkillsProvider) && !slices.Contains(pkg.Capabilities, PluginCapabilitySandboxMounts) {
		return fmt.Errorf("plugin must declare at least one hook (or a capability that gives it another job)")
	}
	for _, id := range pkg.Hooks {
		if _, ok := hookRegistry[id]; !ok {
			return fmt.Errorf("unknown hook %q", id)
		}
	}
	if err := requireSidecarRuntime(pkg, "plugin"); err != nil {
		return err
	}
	// A plugin runs once per signed-in account on a port the desktop picks
	// (docs/prd/2026-10-22-multi-account.md §2.4), told in listenEnv: the
	// package's localUrl is the declared shape, never an address it may bind.
	if pkg.Sidecar == nil || strings.TrimSpace(pkg.Sidecar.ListenEnv) == "" {
		return fmt.Errorf("plugin sidecar must declare listenEnv: it runs once per account on a port the desktop picks and tells it there")
	}
	if err := validatePluginJobs(pkg); err != nil {
		return err
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
	return nil
}

// validatePluginJobs checks the launch fields the capabilities that are not
// about hooks need, and refuses the ones that would be unused.
func validatePluginJobs(pkg Package) error {
	sc := pkg.Sidecar
	mounts := slices.Contains(pkg.Capabilities, PluginCapabilitySandboxMounts)
	if mounts && (sc.MountsEnv == "" || sc.MountRootEnv == "") {
		return fmt.Errorf("a %s plugin requires sidecar.mountsEnv and sidecar.mountRootEnv", PluginCapabilitySandboxMounts)
	}
	if !mounts && (sc.MountsEnv != "" || sc.MountRootEnv != "") {
		return fmt.Errorf("sidecar.mountsEnv and sidecar.mountRootEnv need the %s capability", PluginCapabilitySandboxMounts)
	}
	if slices.Contains(pkg.Capabilities, PluginCapabilitySkillsProvider) && sc.TokenEnv == "" {
		return fmt.Errorf("a %s plugin requires sidecar.tokenEnv (its bearer)", PluginCapabilitySkillsProvider)
	}
	return nil
}

func validateAuthProviderPackage(pkg Package) error {
	return validateEndpointPackage(pkg, "auth-provider", AuthProviderContractV1)
}

func validateNotifierPackage(pkg Package) error {
	for _, c := range pkg.Capabilities {
		if !notifierCapabilities[c] {
			return fmt.Errorf("unknown notifier capability %q", c)
		}
	}
	return validateEndpointPackage(pkg, "notifier", NotifierContractV1)
}

// validateEndpointPackage is the one shape rule of every endpoint component
// kind (auth-provider, notifier): the kind's contract and a guarded runtime
// "endpoint" (requireGuardedEndpoint). label names the kind in errors.
func validateEndpointPackage(pkg Package, label, contract string) error {
	if pkg.Contract != contract {
		return fmt.Errorf("unknown %s contract %q", label, pkg.Contract)
	}
	if pkg.Runtime != RuntimeEndpoint {
		return fmt.Errorf(`%s runtime must be "endpoint"`, label)
	}
	return requireGuardedEndpoint(pkg, label)
}

func validateShareTargetPackage(pkg Package) error {
	return validateEndpointPackage(pkg, "share-target", ShareTargetContractV1)
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
	RegisterKindValidator(KindShareTargetPackageV1, validateShareTargetPackage)
	extreg.RegisterKind(extreg.KindDescriptor{
		Kind:  KindPlugin,
		Hosts: []extreg.Service{extreg.ServiceRealtime},
	})
}
