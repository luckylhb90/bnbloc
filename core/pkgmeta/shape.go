package pkgmeta

import (
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/extgov"
)

// kindField is one row of the field-to-kinds table: a payload field only
// the listed kinds may declare.
type kindField struct {
	field string
	set   func(Package) bool
	kinds []string
}

// kindFields is the field-to-kinds table. validatePackage refuses a field on
// any kind not listed, so a stray field (hooks on a core, a manifest on a
// notifier, a contract on a core) never installs silently; a kind
// validator checks only the fields its kind may carry.
var kindFields = []kindField{
	{"contract", func(p Package) bool { return p.Contract != "" },
		[]string{KindPluginPackageV1, KindAuthProviderPackageV1, KindNotifierPackageV1, KindSandboxPackageV1, KindPreviewerPackageV1, KindMCPPackageV1}},
	{"protocol", func(p Package) bool { return p.Protocol.Min != "" || p.Protocol.Max != "" },
		[]string{KindCorePackageV1, KindSandboxPackageV1}},
	{"manifest", func(p Package) bool { return p.Manifest != nil }, []string{KindCorePackageV1}},
	{"sidecar", func(p Package) bool { return p.Sidecar != nil },
		[]string{KindCorePackageV1, KindPluginPackageV1, KindSandboxPackageV1}},
	{"endpoint", func(p Package) bool { return p.Endpoint != nil },
		[]string{KindCorePackageV1, KindAuthProviderPackageV1, KindNotifierPackageV1, KindSandboxPackageV1, KindMCPPackageV1}},
	{"capabilities", func(p Package) bool { return len(p.Capabilities) > 0 },
		[]string{KindCorePackageV1, KindPluginPackageV1, KindSandboxPackageV1, KindNotifierPackageV1}},
	{"hooks", func(p Package) bool { return len(p.Hooks) > 0 }, []string{KindPluginPackageV1}},
	{"settings", func(p Package) bool { return len(p.Settings) > 0 }, []string{KindPluginPackageV1, KindMCPPackageV1}},
	{"settingsPage", func(p Package) bool { return p.SettingsPage != nil }, []string{KindPluginPackageV1, KindMCPPackageV1}},
	{"permissions.host", func(p Package) bool { return len(p.Permissions.Host) > 0 }, []string{KindPluginPackageV1}},
	{"guest", func(p Package) bool { return p.Guest != nil }, []string{KindSandboxPackageV1}},
	{"previewer", func(p Package) bool { return p.Previewer != nil }, []string{KindPreviewerPackageV1}},
	{"mcp", func(p Package) bool { return p.MCP != nil }, []string{KindMCPPackageV1}},
}

// validateKindFields applies the field-to-kinds table.
func validateKindFields(pkg Package) error {
	for _, f := range kindFields {
		if !f.set(pkg) {
			continue
		}
		allowed := false
		for _, k := range f.kinds {
			allowed = allowed || k == pkg.Kind
		}
		if allowed {
			continue
		}
		if len(f.kinds) == 1 {
			return fmt.Errorf("%s is only valid for %s packages", f.field, f.kinds[0])
		}
		return fmt.Errorf("%s packages must not declare %s", pkg.Kind, f.field)
	}
	return nil
}

// RequireEndpointRuntime is the one rule of runtime "endpoint": an
// externally run service at an absolute http(s) endpoint.baseUrl, with
// nothing bnbloc downloads or supervises (no artifacts, no sidecar). label
// names the kind in errors. The install gate and an endpoint component's
// activation (internal/endpointpkg) both apply it.
func RequireEndpointRuntime(pkg Package, label string) error {
	if len(pkg.Artifacts) > 0 {
		return fmt.Errorf(`%s runtime "endpoint" must not declare artifacts`, label)
	}
	if pkg.Sidecar != nil {
		return fmt.Errorf(`%s runtime "endpoint" must not declare sidecar`, label)
	}
	if pkg.Endpoint == nil || strings.TrimSpace(pkg.Endpoint.BaseURL) == "" {
		return fmt.Errorf(`%s runtime "endpoint" requires endpoint.baseUrl`, label)
	}
	if err := validateAbsoluteHTTPURL(pkg.Endpoint.BaseURL); err != nil {
		return fmt.Errorf("endpoint.baseUrl must be an absolute http(s) URL")
	}
	return nil
}

// requireGuardedEndpoint is RequireEndpointRuntime for an endpoint bnbloc
// reaches only through the package's network allowlist (extgov.NetGuard):
// the base URL's host must be in permissions.network.
func requireGuardedEndpoint(pkg Package, label string) error {
	if err := RequireEndpointRuntime(pkg, label); err != nil {
		return err
	}
	if len(pkg.Permissions.Network) == 0 {
		return fmt.Errorf(`%s runtime "endpoint" requires permissions.network`, label)
	}
	if !extgov.NewNetGuard(pkg.Permissions.Network).AllowURL(pkg.Endpoint.BaseURL) {
		return fmt.Errorf("%s endpoint host is not in permissions.network", label)
	}
	return nil
}

// requireSidecarRuntime is the one rule of runtime "sidecar": per-platform
// artifacts the member's desktop downloads and supervises, listening on a
// loopback http sidecar.localUrl, and no endpoint.
func requireSidecarRuntime(pkg Package, label string) error {
	if len(pkg.Artifacts) == 0 {
		return fmt.Errorf("%s sidecar requires artifacts", label)
	}
	if pkg.Sidecar == nil || strings.TrimSpace(pkg.Sidecar.LocalURL) == "" {
		return fmt.Errorf("%s sidecar requires sidecar.localUrl", label)
	}
	if err := ValidateLoopbackHTTPURL(pkg.Sidecar.LocalURL); err != nil {
		return fmt.Errorf("sidecar.localUrl must be a loopback http URL")
	}
	if pkg.Endpoint != nil {
		return fmt.Errorf("%s sidecar must not declare endpoint", label)
	}
	return nil
}
