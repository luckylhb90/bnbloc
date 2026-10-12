package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

// coresPublishMCP publishes an MCP tool component (bnbloc.mcp.package/v1,
// docs/prd/2026-10-12-experts-and-teams.md §6): the package JSON names the
// tools, env, credentials source and runtime; a stdio tool's per-platform
// binaries are uploaded with --binary (the publish-sidecar upload path),
// pinned in the package, signed and installed through the one publish call.
func coresPublishMCP(args []string) error {
	fs := flag.NewFlagSet("cores publish-mcp", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "gateway base URL")
	token := fs.String("token", "", "super-admin session token")
	packageFile := fs.String("package", "", "MCP package JSON (kind/contract default to the MCP kind; a stdio package's artifacts come from --binary)")
	seckey := fs.String("seckey", "", "ed25519 private key file for signing (default "+defaultSeckeyHint+" when it exists)")
	unsigned := fs.Bool("unsigned", false, "install without a signature (dev; gateway must allow it)")
	rollout := fs.String("rollout", "", "rollout set atomically with the publish: auto | optin | off (default: gateway default, auto)")
	var binaries multiFlag
	fs.Var(&binaries, "binary", "stdio server binary as <goos-goarch>=<path> (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validRolloutFlag(*rollout); err != nil {
		return err
	}
	pkg, err := mcpPublishInputs(*packageFile, binaries)
	if err != nil {
		return err
	}
	priv, err := packageSigningKey(*seckey, *unsigned)
	if err != nil {
		return err
	}
	c := newClient(resolveConfig(*gateway, *token))
	if len(binaries) > 0 {
		if pkg.Artifacts, err = uploadArtifacts(c, binaries); err != nil {
			return err
		}
	}
	if err := pkgmeta.ValidatePackage(pkg); err != nil {
		return err
	}
	body, err := json.Marshal(pkg)
	if err != nil {
		return err
	}
	signature, err := signPackageBody(body, priv)
	if err != nil {
		return err
	}
	if err := publishComponent(c, body, signature, *rollout); err != nil {
		return err
	}
	fmt.Printf("published MCP tool %q %s (%s, %d tool(s))\n", pkg.Name, pkg.Version, pkg.Runtime, len(pkg.MCP.Tools))
	return nil
}

// mcpPublishInputs reads and checks the package before any upload: an MCP
// package (kind and contract filled when omitted), valid apart from its
// binary pins; a stdio package needs --binary per platform (each staged as
// an upload placeholder until uploaded), an endpoint package takes none.
func mcpPublishInputs(packageFile string, binaries multiFlag) (pkgmeta.Package, error) {
	if packageFile == "" {
		return pkgmeta.Package{}, fmt.Errorf("usage: cores publish-mcp --package <mcp.json> [--binary <platform>=<path>...]")
	}
	raw, err := os.ReadFile(expandHome(packageFile))
	if err != nil {
		return pkgmeta.Package{}, err
	}
	var pkg pkgmeta.Package
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pkg); err != nil {
		return pkgmeta.Package{}, fmt.Errorf("package json invalid: %w", err)
	}
	if pkg.Kind == "" {
		pkg.Kind = pkgmeta.KindMCPPackageV1
	}
	if pkg.Contract == "" {
		pkg.Contract = pkgmeta.MCPContractV1
	}
	if pkg.Kind != pkgmeta.KindMCPPackageV1 {
		return pkgmeta.Package{}, fmt.Errorf("package kind %q is not %s", pkg.Kind, pkgmeta.KindMCPPackageV1)
	}
	switch pkg.Runtime {
	case pkgmeta.RuntimeStdio:
		if len(binaries) == 0 {
			return pkgmeta.Package{}, fmt.Errorf("a stdio MCP tool needs at least one --binary <platform>=<path>")
		}
		pkg.Artifacts = map[string]pkgmeta.Artifact{}
		for _, b := range binaries {
			platform, path, err := parseBinarySpec(b)
			if err != nil {
				return pkgmeta.Package{}, err
			}
			if _, err := os.Stat(path); err != nil {
				return pkgmeta.Package{}, fmt.Errorf("--binary %s: %w", platform, err)
			}
			pkg.Artifacts[platform] = pkgmeta.Artifact{}
		}
	default:
		if len(binaries) > 0 {
			return pkgmeta.Package{}, fmt.Errorf("--binary is only for a stdio MCP tool (runtime %q)", pkg.Runtime)
		}
	}
	if err := pkgmeta.ValidatePackage(pkg); err != nil {
		return pkgmeta.Package{}, err
	}
	return pkg, nil
}
