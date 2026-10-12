package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	var pins map[string]pkgmeta.Artifact
	if len(binaries) > 0 {
		if pins, err = uploadArtifacts(c, binaries); err != nil {
			return err
		}
	}
	if pkg, err = pkgmeta.AssembleMCP(pkg, pins); err != nil {
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

// mcpPublishInputs reads the package and checks it before any upload
// through the shared assembly (pkgmeta.PrepareMCP): each --binary must name
// an existing file, and the platforms they name are the draft's upload
// placeholders until uploaded.
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
	platforms := make([]string, 0, len(binaries))
	for _, b := range binaries {
		platform, path, err := parseBinarySpec(b)
		if err != nil {
			return pkgmeta.Package{}, err
		}
		if _, err := os.Stat(path); err != nil {
			return pkgmeta.Package{}, fmt.Errorf("--binary %s: %w", platform, err)
		}
		platforms = append(platforms, platform)
	}
	pkg, err = pkgmeta.PrepareMCP(pkg, platforms)
	switch {
	case errors.Is(err, pkgmeta.ErrMCPNeedsBinaries):
		return pkgmeta.Package{}, fmt.Errorf("%w: pass at least one --binary <platform>=<path>", err)
	case errors.Is(err, pkgmeta.ErrMCPEndpointBinaries):
		return pkgmeta.Package{}, fmt.Errorf("--binary is only for a stdio MCP tool: %w", err)
	}
	return pkg, err
}
