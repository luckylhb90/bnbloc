package pkgmeta

import (
	"fmt"
	"strings"

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
	if err := sandboxprotocol.Compatible(pkg.Protocol); err != nil {
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
	if err := validateSandboxGuest(pkg.Guest); err != nil {
		return err
	}
	switch pkg.Runtime {
	case RuntimeSidecar:
		if err := requireSidecarRuntime(pkg, "sandbox"); err != nil {
			return err
		}
		if strings.TrimSpace(pkg.Sidecar.TokenEnv) == "" {
			return fmt.Errorf(`sandbox runtime "sidecar" requires sidecar.tokenEnv (the per-launch bearer)`)
		}
	case RuntimeEndpoint:
		return requireGuardedEndpoint(pkg, "sandbox")
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
