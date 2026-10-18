package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

// componentsPublishPackage publishes a component whose package is a file and
// whose binaries are built per platform (a plugin, a notifier sidecar, any kind
// with a sidecar): it uploads each --binary, pins the artifacts in the package,
// signs it and installs it. The file is the package without `artifacts`; the
// flags of publish-sidecar are the same for the signing and the rollout.
//
//	bnbloc components publish-package --package package.json \
//	    --binary darwin-arm64=dist/bnbloc-local-skills-darwin-arm64 --rollout optin
//
// A file the sidecar needs beside its executable (sidecar.files) is a
// --binary <platform>/<name>=<path>.
func componentsPublishPackage(args []string) error {
	fs := flag.NewFlagSet("components publish-package", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	file := fs.String("package", "", "package JSON without artifacts")
	sign := addSigningFlags(fs, "rollout set atomically with the publish: auto | optin | off (default: gateway default, auto)")
	var binaries multiFlag
	fs.Var(&binaries, "binary", "platform binary as <goos-goarch>=<path>, or <goos-goarch>/<file>=<path> for a declared sidecar file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pkg, err := packageTemplate(*file, binaries)
	if err != nil {
		return err
	}
	if err := validRolloutFlag(*sign.rollout); err != nil {
		return err
	}
	priv, err := sign.key()
	if err != nil {
		return err
	}
	c := gw.client()
	artifacts, err := uploadArtifacts(c, binaries)
	if err != nil {
		return err
	}
	pkg.Artifacts = artifacts
	body, err := json.Marshal(pkg)
	if err != nil {
		return err
	}
	signature, err := signPackageBody(body, priv)
	if err != nil {
		return err
	}
	if err := publishComponent(c, body, signature, *sign.rollout); err != nil {
		return err
	}
	fmt.Printf("published %s %q %s (%d artifact(s))\n", pkg.Kind, pkg.Name, pkg.Version, len(artifacts))
	return nil
}

// packageTemplate reads the package file and holds it, with a placeholder per
// --binary, to the install gate before any byte is uploaded, so a bad package
// leaves no orphan upload.
func packageTemplate(file string, binaries multiFlag) (pkgmeta.Package, error) {
	if file == "" {
		return pkgmeta.Package{}, fmt.Errorf("--package is required")
	}
	if len(binaries) == 0 {
		return pkgmeta.Package{}, fmt.Errorf("at least one --binary <platform>=<path> is required")
	}
	raw, err := os.ReadFile(expandHome(file))
	if err != nil {
		return pkgmeta.Package{}, err
	}
	var pkg pkgmeta.Package
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return pkgmeta.Package{}, fmt.Errorf("package json invalid: %w", err)
	}
	if len(pkg.Artifacts) != 0 {
		return pkgmeta.Package{}, fmt.Errorf("the package file must not name artifacts: they are the --binary uploads")
	}
	pkg.Artifacts = map[string]pkgmeta.Artifact{}
	for _, b := range binaries {
		key, _, err := parseBinarySpec(b)
		if err != nil {
			return pkgmeta.Package{}, err
		}
		pkg.Artifacts[key] = pkgmeta.Artifact{}
	}
	if err := pkgmeta.ValidatePackage(pkg); err != nil {
		return pkgmeta.Package{}, fmt.Errorf("package invalid: %w", err)
	}
	return pkg, nil
}
