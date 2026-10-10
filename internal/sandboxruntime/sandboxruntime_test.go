package sandboxruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	sandboxprotocol "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

func sidecarPackage() pkgmeta.Package {
	art := func(c string) pkgmeta.Artifact {
		return pkgmeta.Artifact{URL: "https://cdn.example/" + c, SHA256: strings.Repeat(c, 64)}
	}
	return pkgmeta.Package{
		Kind: pkgmeta.KindSandboxPackageV1, Name: "sandboxd", Version: "0.1.0", Publisher: "Acme",
		Protocol: sandboxprotocol.ProtocolRange{Min: "1.0", Max: "1.0"},
		Runtime:  pkgmeta.RuntimeSidecar, Contract: pkgmeta.SandboxContractV1,
		Capabilities: []string{sandboxprotocol.CapabilityExec, sandboxprotocol.CapabilitySnapshots},
		Artifacts:    map[string]pkgmeta.Artifact{"darwin-arm64": art("a"), "darwin-arm64/msb": art("b")},
		Sidecar: &pkgmeta.Sidecar{
			LocalURL: "http://127.0.0.1:7311", TokenEnv: "SBX_TOKEN", SupervisedEnv: "SBX_SUPERVISED",
			Files: []pkgmeta.SidecarFile{{Name: "msb", Env: "SBX_MSB_PATH", Executable: true}},
		},
	}
}

func endpointPackage() pkgmeta.Package {
	pkg := sidecarPackage()
	pkg.Runtime, pkg.Artifacts, pkg.Sidecar = pkgmeta.RuntimeEndpoint, nil, nil
	pkg.Endpoint = &pkgmeta.Endpoint{BaseURL: "https://sandboxes.example.com"}
	pkg.Permissions = pkgmeta.Permissions{Network: []string{"sandboxes.example.com"}}
	return pkg
}

func record(t *testing.T, pkg pkgmeta.Package) extpipe.Record {
	t.Helper()
	raw, backend, err := DecodePackage(pkg)
	if err != nil {
		t.Fatal(err)
	}
	return extpipe.Record{Kind: string(extreg.KindSandbox), Name: pkg.Name, Version: pkg.Version, Publisher: pkg.Publisher,
		Backend: string(backend), Config: raw, Network: pkg.Permissions.Network}
}

func guard(rec extpipe.Record) extgov.Guard {
	return extgov.Guard{Net: extgov.NewNetGuard(rec.Network)}
}

func TestProbeRecord_acceptsValidSidecarAndEndpoint(t *testing.T) {
	for name, pkg := range map[string]pkgmeta.Package{"sidecar": sidecarPackage(), "endpoint": endpointPackage()} {
		t.Run(name, func(t *testing.T) {
			rec := record(t, pkg)
			if err := ProbeRecord(context.Background(), rec, guard(rec)); err != nil {
				t.Fatalf("probe: %v", err)
			}
		})
	}
}

func TestProbeRecord_roundTripsLaunchMetadata(t *testing.T) {
	// Given a decoded sidecar record.
	rec := record(t, sidecarPackage())

	// When the package is reassembled from the persisted config.
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		t.Fatal(err)
	}
	pkg := cfg.Package(pkgmeta.KindSandboxPackageV1, rec)

	// Then nothing the desktop needs was lost.
	if pkg.Contract != pkgmeta.SandboxContractV1 || pkg.Protocol.Max != "1.0" || len(pkg.Capabilities) != 2 ||
		pkg.Sidecar.TokenEnv != "SBX_TOKEN" || len(pkg.Sidecar.Files) != 1 || pkg.Artifacts["darwin-arm64/msb"].SHA256 == "" {
		t.Fatalf("round trip lost data: %+v", pkg)
	}
}

func TestProbeRecord_rejectsContractViolations(t *testing.T) {
	tests := map[string]func(*pkgmeta.Package){
		"unknown contract":  func(p *pkgmeta.Package) { p.Contract = "bnbloc.sandbox.contract/v0" },
		"missing tokenEnv":  func(p *pkgmeta.Package) { p.Sidecar.TokenEnv = "" },
		"non-loopback":      func(p *pkgmeta.Package) { p.Sidecar.LocalURL = "http://10.1.2.3:7311" },
		"no exec":           func(p *pkgmeta.Package) { p.Capabilities = []string{sandboxprotocol.CapabilityPorts} },
		"missing file blob": func(p *pkgmeta.Package) { delete(p.Artifacts, "darwin-arm64/msb") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			pkg := sidecarPackage()
			mutate(&pkg)
			rec := record(t, pkg)
			if err := ProbeRecord(context.Background(), rec, guard(rec)); err == nil {
				t.Fatal("invalid sandbox record accepted")
			}
		})
	}
}

func TestProbeRecord_rejectsEndpointOutsideRecordNetwork(t *testing.T) {
	// Given an endpoint record whose stored network allowlist no longer covers its host.
	rec := record(t, endpointPackage())
	rec.Network = []string{"elsewhere.example.com"}

	// When the guard is built from the record (as the pipeline does).
	err := ProbeRecord(context.Background(), rec, extgov.Guard{Net: extgov.NewNetGuard([]string{"elsewhere.example.com"})})

	// Then
	if err == nil || !strings.Contains(err.Error(), "permissions.network") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstaller_activatesNothingInTheGateway(t *testing.T) {
	in := Installer()
	inst, err := in.Activate(context.Background(), record(t, sidecarPackage()), extgov.Guard{})
	if err != nil || inst != nil {
		t.Fatalf("activate = %v, %v; want nil, nil", inst, err)
	}
	if in.Kind != extreg.KindSandbox || len(in.PackageKinds) != 1 || in.PackageKinds[0] != pkgmeta.KindSandboxPackageV1 {
		t.Fatalf("installer = %+v", in)
	}
}
