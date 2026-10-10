package pkgmeta

import (
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/extgov"
	sandboxprotocol "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// KindSandboxPackageV1 is the package-envelope kind for a sandbox runtime: an
// implementation of Sandbox Protocol v1 (docs/architecture/sandbox-protocol.md).
// Locally it is a sidecar the desktop supervises (runtime "sidecar": artifacts
// per platform, loopback localUrl, a per-launch bearer via tokenEnv, plus any
// declared extra files such as a VMM helper and firmware library); remotely it
// is an externally run service (runtime "endpoint").
const KindSandboxPackageV1 = "bnbloc.sandbox.package/v1"

// SandboxContractV1 is the wire contract a sandbox package declares; it is the
// Sandbox Protocol contract the implementation's discovery echoes.
const SandboxContractV1 = sandboxprotocol.ContractV1

func validateSandboxPackage(pkg Package) error {
	if pkg.Contract != SandboxContractV1 {
		return fmt.Errorf("unknown sandbox contract %q", pkg.Contract)
	}
	local := sandboxprotocol.ProtocolRange{Min: sandboxprotocol.SupportedMinVersion, Max: sandboxprotocol.SupportedMaxVersion}
	if err := compatibleProtocolRange("sandbox protocol", local, pkg.Protocol); err != nil {
		return err
	}
	hasExec := false
	for _, c := range pkg.Capabilities {
		if !sandboxprotocol.KnownCapability(c) {
			return fmt.Errorf("unknown sandbox capability %q", c)
		}
		hasExec = hasExec || c == sandboxprotocol.CapabilityExec
	}
	if !hasExec {
		return fmt.Errorf("sandbox package must declare the %q capability", sandboxprotocol.CapabilityExec)
	}
	if len(pkg.Hooks) > 0 {
		return fmt.Errorf("hooks are only valid for %s packages", KindPluginPackageV1)
	}
	if err := validateSandboxGuest(pkg.Guest); err != nil {
		return err
	}
	switch pkg.Runtime {
	case RuntimeSidecar:
		if len(pkg.Artifacts) == 0 {
			return fmt.Errorf(`sandbox runtime "sidecar" requires artifacts`)
		}
		if pkg.Sidecar == nil || strings.TrimSpace(pkg.Sidecar.LocalURL) == "" {
			return fmt.Errorf(`sandbox runtime "sidecar" requires sidecar.localUrl`)
		}
		if err := ValidateLoopbackHTTPURL(pkg.Sidecar.LocalURL); err != nil {
			return fmt.Errorf("sidecar.localUrl must be a loopback http URL")
		}
		if strings.TrimSpace(pkg.Sidecar.TokenEnv) == "" {
			return fmt.Errorf(`sandbox runtime "sidecar" requires sidecar.tokenEnv (the per-launch bearer)`)
		}
		if pkg.Endpoint != nil {
			return fmt.Errorf(`sandbox runtime "sidecar" must not declare endpoint`)
		}
	case RuntimeEndpoint:
		if len(pkg.Artifacts) > 0 {
			return fmt.Errorf(`sandbox runtime "endpoint" must not declare artifacts`)
		}
		if pkg.Sidecar != nil {
			return fmt.Errorf(`sandbox runtime "endpoint" must not declare sidecar`)
		}
		if pkg.Endpoint == nil || strings.TrimSpace(pkg.Endpoint.BaseURL) == "" {
			return fmt.Errorf(`sandbox runtime "endpoint" requires endpoint.baseUrl`)
		}
		if err := validateAbsoluteHTTPURL(pkg.Endpoint.BaseURL); err != nil {
			return fmt.Errorf("endpoint.baseUrl must be an absolute http(s) URL")
		}
		if len(pkg.Permissions.Network) == 0 {
			return fmt.Errorf(`sandbox runtime "endpoint" requires permissions.network`)
		}
		if !extgov.NewNetGuard(pkg.Permissions.Network).AllowURL(pkg.Endpoint.BaseURL) {
			return fmt.Errorf("sandbox endpoint host is not in permissions.network")
		}
	default:
		return fmt.Errorf(`sandbox runtime must be "sidecar" or "endpoint", got %q`, pkg.Runtime)
	}
	return nil
}

// validateSandboxGuest checks the optional guest profile: an OCI image
// reference (implementations refuse host paths, so a package must not name
// one) and non-negative sizing.
func validateSandboxGuest(g *Guest) error {
	if g == nil {
		return nil
	}
	image := strings.TrimSpace(g.Image)
	switch {
	case image == "":
		return fmt.Errorf("guest.image required")
	case image != g.Image || strings.ContainsAny(image, " \t\n"):
		return fmt.Errorf("guest.image must not contain whitespace")
	case strings.HasPrefix(image, "/") || strings.HasPrefix(image, ".") || strings.HasPrefix(image, "~") || strings.Contains(image, "://"):
		return fmt.Errorf("guest.image %q must be an OCI image reference, not a path or URL", image)
	}
	if g.CPUs < 0 || g.MemoryMiB < 0 {
		return fmt.Errorf("guest.cpus and guest.memoryMiB must not be negative")
	}
	return nil
}

func init() { RegisterKindValidator(KindSandboxPackageV1, validateSandboxPackage) }
