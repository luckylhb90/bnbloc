package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

func coresPublishSidecar(args []string) error {
	fs := flag.NewFlagSet("cores publish-sidecar", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	name := fs.String("name", "", "core name")
	version := fs.String("version", "0.1.0", "core version (semver)")
	publisher := fs.String("publisher", "self", "publisher id")
	manifestFile := fs.String("manifest", "", "manifest JSON file (legacy REST drive contract; omit for a protocol-conforming sidecar)")
	localURL := fs.String("local-url", "", "loopback URL the desktop bridge reaches after launch")
	listenEnv := fs.String("listen-env", "", "environment variable used to tell the sidecar where to listen")
	supervisedEnv := fs.String("supervised-env", "", "environment variable the desktop sets to 1 so the sidecar exits when its stdin closes")
	sign := addSigningFlags(fs, "rollout set atomically with the publish: auto | optin | off (default: gateway default, auto)")
	var binaries multiFlag
	fs.Var(&binaries, "binary", "platform binary as <goos-goarch>=<path> (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pkg, err := sidecarPackage(sidecarSpec{
		Name: *name, Version: *version, Publisher: *publisher, ManifestFile: *manifestFile,
		LocalURL: *localURL, ListenEnv: *listenEnv, SupervisedEnv: *supervisedEnv, Binaries: binaries,
	})
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
	pkgBody, err := json.Marshal(pkg)
	if err != nil {
		return err
	}
	signature, err := signPackageBody(pkgBody, priv)
	if err != nil {
		return err
	}
	if err := publishComponent(c, pkgBody, signature, *sign.rollout); err != nil {
		return err
	}
	fmt.Printf("published sidecar core %q %s (%d platform(s))\n", pkg.Name, pkg.Version, len(artifacts))
	return nil
}

// sidecarSpec is the flag-level description of a sidecar core package.
type sidecarSpec struct {
	Name, Version, Publisher string
	ManifestFile             string
	LocalURL, ListenEnv      string
	SupervisedEnv            string
	Binaries                 multiFlag
}

// sidecarPackage maps the publish-sidecar flags onto the package: each
// --binary's platform is an upload placeholder until uploaded. It holds the
// draft to the install gate (pkgmeta.ValidatePackage) before any byte is
// uploaded, so a bad name or URL leaves no orphan upload.
func sidecarPackage(spec sidecarSpec) (pkgmeta.Package, error) {
	if spec.Name == "" {
		return pkgmeta.Package{}, fmt.Errorf("--name is required")
	}
	if len(spec.Binaries) == 0 {
		return pkgmeta.Package{}, fmt.Errorf("at least one --binary <platform>=<path> is required")
	}
	for _, b := range spec.Binaries {
		if _, _, err := parseBinarySpec(b); err != nil {
			return pkgmeta.Package{}, err
		}
	}
	m, err := readOptionalManifest(spec.ManifestFile)
	if err != nil {
		return pkgmeta.Package{}, err
	}
	localURL := strings.TrimSpace(spec.LocalURL)
	if localURL == "" && m != nil && m.Discover != nil {
		localURL = strings.TrimSpace(m.Discover.ProbeURL)
	}
	if localURL == "" {
		return pkgmeta.Package{}, fmt.Errorf("--local-url is required (no manifest to derive it from)")
	}
	pkg := pkgmeta.Package{
		Kind:      pkgmeta.KindCorePackageV1,
		Name:      spec.Name,
		Version:   spec.Version,
		Runtime:   pkgmeta.RuntimeSidecar,
		Protocol:  protocol.ProtocolRange{Min: protocol.SupportedMinVersion, Max: protocol.SupportedMaxVersion},
		Manifest:  m,
		Sidecar:   &pkgmeta.Sidecar{LocalURL: localURL, ListenEnv: strings.TrimSpace(spec.ListenEnv), SupervisedEnv: strings.TrimSpace(spec.SupervisedEnv)},
		Artifacts: map[string]pkgmeta.Artifact{},
		Publisher: spec.Publisher,
	}
	for _, b := range spec.Binaries {
		platform, _, _ := parseBinarySpec(b)
		pkg.Artifacts[platform] = pkgmeta.Artifact{}
	}
	if err := pkgmeta.ValidatePackage(pkg); err != nil {
		return pkgmeta.Package{}, fmt.Errorf("package invalid: %w", err)
	}
	return pkg, nil
}

// parseBinarySpec splits a --binary <platform>=<path> value.
func parseBinarySpec(spec string) (platform, path string, err error) {
	platform, path, ok := strings.Cut(spec, "=")
	if !ok || platform == "" || path == "" {
		return "", "", fmt.Errorf("--binary must be <platform>=<path>, got %q", spec)
	}
	return platform, expandHome(path), nil
}

func uploadArtifacts(c *client, binaries multiFlag) (map[string]pkgmeta.Artifact, error) {
	artifacts := map[string]pkgmeta.Artifact{}
	for _, spec := range binaries {
		platform, path, err := parseBinarySpec(spec)
		if err != nil {
			return nil, err
		}
		up, err := uploadArtifactFile(c, path)
		if err != nil {
			return nil, fmt.Errorf("upload %s (%s): %w", platform, path, err)
		}
		artifacts[platform] = pkgmeta.Artifact{URL: up.URL, SHA256: up.SHA256}
		fmt.Printf("uploaded %s -> %s\n", platform, up.Key)
	}
	return artifacts, nil
}

func readOptionalManifest(manifestFile string) (*manifestcore.Manifest, error) {
	if strings.TrimSpace(manifestFile) == "" {
		return nil, nil
	}
	rawManifest, err := os.ReadFile(expandHome(manifestFile))
	if err != nil {
		return nil, err
	}
	m := &manifestcore.Manifest{}
	if err := json.Unmarshal(rawManifest, m); err != nil {
		return nil, fmt.Errorf("manifest json invalid: %w", err)
	}
	return m, nil
}
