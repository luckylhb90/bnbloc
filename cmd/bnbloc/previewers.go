package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// runPreviewers handles previewer component packaging
// (bnbloc.previewer.package/v1, docs/architecture/previewer-components.md):
//
//	pack <dir> --out <bundle.tar>       archive a built previewer directory
//	publish --package <pkg.json> (--bundle <tar> | --dir <dir>)
//	                                    upload the bundle, pin it, sign, install
func runPreviewers(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("previewers: expected a subcommand (pack|publish)")
	}
	switch args[0] {
	case "pack":
		return previewersPack(args[1:])
	case "publish":
		return previewersPublish(args[1:])
	default:
		return fmt.Errorf("previewers: unknown subcommand %q", args[0])
	}
}

func previewersPack(args []string) error {
	fs := flag.NewFlagSet("previewers pack", flag.ContinueOnError)
	out := fs.String("out", "", "bundle archive to write (.tar)")
	dir, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if dir == "" || *out == "" {
		return fmt.Errorf("usage: previewers pack <dir> --out <bundle.tar>")
	}
	raw, err := tarbundle.Pack(expandHome(dir), tarbundle.PreviewerLimits)
	if err != nil {
		return err
	}
	b, err := tarbundle.Parse(raw, "", tarbundle.PreviewerLimits)
	if err != nil {
		return err
	}
	if err := os.WriteFile(expandHome(*out), raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d files, %d bytes)\nsha256 %s\n", *out, len(b.Paths()), len(raw), b.SHA256)
	return nil
}

func previewersPublish(args []string) error {
	fs := flag.NewFlagSet("previewers publish", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "gateway base URL")
	token := fs.String("token", "", "super-admin session token")
	packageFile := fs.String("package", "", "previewer package JSON (artifacts.bundle may be a {url:\"\",sha256:\"\"} placeholder)")
	bundleFile := fs.String("bundle", "", "built bundle archive (.tar)")
	dir := fs.String("dir", "", "built previewer directory to pack instead of --bundle")
	seckey := fs.String("seckey", "", "ed25519 private key file for signing (default "+defaultSeckeyHint+" when it exists)")
	unsigned := fs.Bool("unsigned", false, "install without a signature (dev; gateway must allow it)")
	rollout := fs.String("rollout", "", "rollout set atomically with the publish: auto | optin | off (default: gateway default, auto)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validRolloutFlag(*rollout); err != nil {
		return err
	}
	pkg, bundlePath, cleanup, err := previewerPublishInputs(*packageFile, *bundleFile, *dir)
	if err != nil {
		return err
	}
	defer cleanup()
	priv, err := packageSigningKey(*seckey, *unsigned)
	if err != nil {
		return err
	}
	c := newClient(resolveConfig(*gateway, *token))
	up, err := uploadArtifactFile(c, bundlePath)
	if err != nil {
		return fmt.Errorf("upload bundle: %w", err)
	}
	pkg.Artifacts = map[string]pkgmeta.Artifact{pkgmeta.PreviewerBundleArtifact: {URL: up.URL, SHA256: up.SHA256}}
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
	fmt.Printf("published previewer %q %s (bundle %s)\n", pkg.Name, pkg.Version, up.Key)
	return nil
}

// previewerPublishInputs reads and checks everything before any upload: the
// package (a previewer, valid apart from its bundle pin) and the bundle
// archive (packed from --dir into a temp file when given), which must hold
// the package's entry. cleanup removes a temp archive.
func previewerPublishInputs(packageFile, bundleFile, dir string) (pkgmeta.Package, string, func(), error) {
	noop := func() {}
	if packageFile == "" || (bundleFile == "") == (dir == "") {
		return pkgmeta.Package{}, "", noop, fmt.Errorf("usage: previewers publish --package <pkg.json> (--bundle <tar> | --dir <dir>)")
	}
	rawPkg, err := os.ReadFile(expandHome(packageFile))
	if err != nil {
		return pkgmeta.Package{}, "", noop, err
	}
	pkg, err := pkgmeta.ParsePackage(rawPkg)
	if err != nil {
		return pkgmeta.Package{}, "", noop, fmt.Errorf("%s: %w", packageFile, err)
	}
	if pkg.Kind != pkgmeta.KindPreviewerPackageV1 {
		return pkgmeta.Package{}, "", noop, fmt.Errorf("%s: kind must be %s", packageFile, pkgmeta.KindPreviewerPackageV1)
	}
	var raw []byte
	path := expandHome(bundleFile)
	cleanup := noop
	if dir != "" {
		if raw, err = tarbundle.Pack(expandHome(dir), tarbundle.PreviewerLimits); err != nil {
			return pkgmeta.Package{}, "", noop, err
		}
		tmp, err := os.MkdirTemp("", "bnbloc-previewer-")
		if err != nil {
			return pkgmeta.Package{}, "", noop, err
		}
		cleanup = func() { _ = os.RemoveAll(tmp) }
		path = filepath.Join(tmp, "bundle.tar")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			cleanup()
			return pkgmeta.Package{}, "", noop, err
		}
	} else if raw, err = os.ReadFile(path); err != nil {
		return pkgmeta.Package{}, "", noop, err
	}
	b, err := tarbundle.Parse(raw, "", tarbundle.PreviewerLimits)
	if err == nil {
		err = b.RequireEntry(pkg.Previewer.Entry)
	}
	if err != nil {
		cleanup()
		return pkgmeta.Package{}, "", noop, err
	}
	return pkg, path, cleanup, nil
}
