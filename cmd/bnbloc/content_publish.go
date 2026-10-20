package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Content from a directory (docs/prd/2026-10-20-no-built-in-content.md):
// the platform ships no content; the default content, and any content an
// operator keeps in a repository, is published at platform scope from its
// package directories. The directory is packed with the platform's one
// packer (tarbundle: junk and hidden files never packed) and validated
// with the one validator, as a save would (an assist under a task's name
// meets its output contract), before anything is uploaded; then the
// bundle is uploaded (POST /v1/platform/artifacts), its platform package
// built (contentpkg.PlatformPackage), signed like `content promote` and
// installed (POST /v1/platform/components). A new version of a package
// adds a version to the same platform row; publishing a version again
// with the same bytes only sets its rollout.
//
//	bnbloc content check [--json] (<dir>... | --all <root>)
//	bnbloc content contracts
//	bnbloc content publish (--dir <dir> | --all <root>) [--publisher p] [--rollout auto|off] [--seckey f | --unsigned]

// contentDir is one package directory, packed and validated.
type contentDir struct {
	Dir     string
	Bundle  []byte
	Package *contentpkg.Package
}

// packContentDir packs dir and validates the bundle as a save would.
func packContentDir(dir string) (contentDir, error) {
	path := expandHome(dir)
	info, err := os.Stat(path)
	if err != nil {
		return contentDir{}, err
	}
	if !info.IsDir() {
		return contentDir{}, fmt.Errorf("%s is not a package directory", dir)
	}
	raw, err := tarbundle.Pack(path, contentpkg.BundleLimits())
	if err != nil {
		return contentDir{}, err
	}
	pkg, err := contentpkg.Validate(raw, "", contentpkg.Options{Contracts: true})
	if err != nil {
		return contentDir{}, err
	}
	return contentDir{Dir: dir, Bundle: raw, Package: pkg}, nil
}

// contentDirsUnder lists root's package directories (every directory
// holding a component.json, hidden and junk ones aside), sorted.
func contentDirsUnder(root string) ([]string, error) {
	entries, err := os.ReadDir(expandHome(root))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || tarbundle.Ignored(e.Name()) || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(expandHome(root), e.Name(), contentpkg.ManifestFile)); err == nil {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no package directory (a directory with %s)", root, contentpkg.ManifestFile)
	}
	sort.Strings(out)
	return out, nil
}

// contentDirsArg is the directories a command names: positionals and
// --dir, or every package under --all.
func contentDirsArg(dirs []string, all string) ([]string, error) {
	if all != "" {
		if len(dirs) > 0 {
			return nil, errors.New("name package directories or --all <root>, not both")
		}
		return contentDirsUnder(all)
	}
	if len(dirs) == 0 {
		return nil, errors.New("a package directory (or --all <root>) is required")
	}
	return dirs, nil
}

// contentCheckResult is one directory's check (--json).
type contentCheckResult struct {
	Dir     string `json:"dir"`
	Kind    string `json:"kind,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Error   string `json:"error,omitempty"`
}

func contentCheck(args []string) error {
	fs := flag.NewFlagSet("content check", flag.ContinueOnError)
	all := fs.String("all", "", "check every package directory under this root")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	dirs, err := contentDirsArg(pos, *all)
	if err != nil {
		return fmt.Errorf("content check: %w", err)
	}
	results := make([]contentCheckResult, 0, len(dirs))
	failed := 0
	for _, dir := range dirs {
		r := contentCheckResult{Dir: dir}
		if d, err := packContentDir(dir); err != nil {
			r.Error = err.Error()
			failed++
		} else {
			m := d.Package.Manifest
			r.Kind, r.Name, r.Version, r.SHA256 = m.Kind, m.Name, m.Version, d.Package.SHA256
		}
		results = append(results, r)
	}
	if *asJSON {
		out, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(out))
	} else {
		for _, r := range results {
			if r.Error != "" {
				fmt.Printf("FAIL %s: %s\n", r.Dir, r.Error)
				continue
			}
			fmt.Printf("ok   %s: %s %s %s\n", r.Dir, shortContentKind(r.Kind), r.Name, r.Version)
		}
	}
	if failed > 0 {
		return fmt.Errorf("content check: %d of %d packages do not validate", failed, len(dirs))
	}
	return nil
}

// shortContentKind is a content kind's short name ("expert").
func shortContentKind(kind string) string {
	if spec, ok := contentpkg.Lookup(kind); ok {
		return spec.Short
	}
	return kind
}

// contentContracts prints the assist output contracts (wire.AssistContracts):
// what an assist saved under a task's name must answer.
func contentContracts(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("content contracts: unexpected arguments %q", args)
	}
	out, err := json.MarshalIndent(wire.AssistContracts, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func contentPublish(args []string) error {
	fs := flag.NewFlagSet("content publish", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	var dirs multiFlag
	fs.Var(&dirs, "dir", "a content package directory (repeatable)")
	all := fs.String("all", "", "publish every package directory under this root")
	publisher := fs.String("publisher", "self", "publisher id written into each package")
	sign := addSigningFlags(fs, "auto (default: every member of every organization) or off (installed unpublished)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fmt.Errorf("content publish: unexpected arguments %q (name a directory with --dir)", pos)
	}
	if r := strings.TrimSpace(*sign.rollout); r != "" && r != "auto" && r != "off" {
		return fmt.Errorf("--rollout must be auto or off for content (members turn content on themselves)")
	}
	paths, err := contentDirsArg(dirs, *all)
	if err != nil {
		return fmt.Errorf("content publish: %w", err)
	}
	// Every package is packed and validated, and the key loaded, before
	// anything is uploaded: a bad package leaves no upload behind.
	packed := make([]contentDir, 0, len(paths))
	for _, dir := range paths {
		d, err := packContentDir(dir)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		packed = append(packed, d)
	}
	priv, err := sign.key()
	if err != nil {
		return err
	}
	c := gw.client()
	for i, d := range packed {
		if err := publishContentDir(c, d, *publisher, *sign.rollout, priv); err != nil {
			return fmt.Errorf("%s: %w (published %d of %d; publishing again is safe: a version already installed with the same bytes is kept)", d.Dir, err, i, len(packed))
		}
		m := d.Package.Manifest
		fmt.Printf("published %s %s %s for every organization\n", shortContentKind(m.Kind), m.Name, m.Version)
	}
	return nil
}

// publishContentDir uploads a packed directory's bundle, pins it in its
// platform package, signs the package and installs it.
func publishContentDir(c *client, d contentDir, publisher, rollout string, priv ed25519.PrivateKey) error {
	tmp, err := os.MkdirTemp("", "bnbloc-content-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, "bundle.tar")
	if err := os.WriteFile(path, d.Bundle, 0o600); err != nil {
		return err
	}
	up, err := uploadArtifactFile(c, path)
	if err != nil {
		return fmt.Errorf("upload the bundle: %w", err)
	}
	if up.SHA256 != d.Package.SHA256 {
		return fmt.Errorf("the uploaded bundle is %s, the packed one %s", up.SHA256, d.Package.SHA256)
	}
	pkg := contentpkg.PlatformPackage(d.Package.Manifest, up.SHA256, up.URL, publisher)
	if err := pkgmeta.ValidatePackage(pkg); err != nil {
		return err
	}
	body, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return err
	}
	signature, err := signPackageBody(body, priv)
	if err != nil {
		return err
	}
	return publishComponent(c, body, signature, rollout)
}
