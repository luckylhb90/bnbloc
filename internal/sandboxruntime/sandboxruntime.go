// Package sandboxruntime is the extpipe lifecycle of sandbox components
// (bnbloc.sandbox.package/v1): Sandbox Protocol v1 runtimes, either a local
// sidecar the member's desktop supervises or a remote endpoint. Nothing runs
// in the gateway: install persists the record so the member catalog
// (GET /v1/components, rollout + tenant gated) can hand the desktop the
// artifacts and launch metadata; the bridge drives the protocol later.
package sandboxruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/endpointpkg"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// Config is a sandbox record's persisted config: the shared package
// projection (runtime, contract, protocol, capabilities, sidecar, artifacts,
// endpoint, permissions).
type Config = extpipe.PackageConfig

// endpointKind is the shared endpoint-component rule set (runtime, contract,
// baseUrl, permissions.network allowlist) applied to endpoint sandboxes.
var endpointKind = endpointpkg.Kind{
	Kind:        extreg.KindSandbox,
	PackageKind: pkgmeta.KindSandboxPackageV1,
	Label:       "sandbox",
	Contract:    pkgmeta.SandboxContractV1,
}

// Installer returns the sandbox kind's lifecycle vtable.
func Installer() extpipe.KindInstaller {
	return extpipe.KindInstaller{
		Kind:         extreg.KindSandbox,
		PackageKinds: []string{pkgmeta.KindSandboxPackageV1},
		Decode:       DecodePackage,
		Probe:        ProbeRecord,
		// Activate registers nothing: the gateway never runs or dials a
		// sandbox. A nil instance keeps the record out of the live registry
		// while the pipeline still tracks it for reconcile.
		Activate: func(context.Context, extpipe.Record, extgov.Guard) (any, error) { return nil, nil },
	}
}

// DecodePackage projects a verified sandbox package onto its persisted config.
func DecodePackage(pkg pkgmeta.Package) (json.RawMessage, extreg.Backend, error) {
	raw, err := extpipe.EncodePackageConfig(pkg)
	if err != nil {
		return nil, "", err
	}
	return raw, extreg.BackendSidecar, nil
}

// ProbeRecord re-validates a sandbox record exactly as the install gate
// validates a package (the reassembled package goes through
// pkgmeta.ValidatePackage), and for an endpoint additionally applies the
// shared endpoint rules under the record's network guard. It does NOT dial:
// every Sandbox Protocol route except healthz requires the implementation's
// bearer, and the gateway holds no sandbox bearer in this phase (a local
// sidecar's token is minted per launch by the member's desktop; a remote
// endpoint's credential is provisioned with the bridge integration).
func ProbeRecord(_ context.Context, rec extpipe.Record, guard extgov.Guard) error {
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		return err
	}
	if err := pkgmeta.ValidatePackage(cfg.Package(pkgmeta.KindSandboxPackageV1, rec)); err != nil {
		return fmt.Errorf("sandbox record invalid: %w", err)
	}
	if cfg.Runtime == pkgmeta.RuntimeEndpoint {
		return endpointKind.Validate(cfg, guard)
	}
	return nil
}
