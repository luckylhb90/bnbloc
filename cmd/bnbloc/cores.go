package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"

	// The content kinds register their install validators with pkgmeta, so
	// a content platform package is held to the gateway's gate here too.
	_ "github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
)

func runCores(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cores: expected a subcommand (install|publish-sidecar|publish-mcp|list|uninstall|enable)")
	}
	switch args[0] {
	case "install":
		return coresInstall(args[1:])
	case "publish-sidecar":
		return coresPublishSidecar(args[1:])
	case "publish-mcp":
		return coresPublishMCP(args[1:])
	case "list":
		return coresList(args[1:])
	case "uninstall":
		return coresUninstall(args[1:])
	case "enable":
		return coresEnable(args[1:])
	default:
		return fmt.Errorf("cores: unknown subcommand %q", args[0])
	}
}

func coresInstall(args []string) error {
	fs := flag.NewFlagSet("cores install", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	daemonURL := fs.String("url", "", "protocol-conforming daemon URL (zero-JSON install)")
	manifestFile := fs.String("manifest", "", "manifest JSON file (legacy REST daemon)")
	packageFile := fs.String("package", "", "pre-built package JSON file")
	signatureFile := fs.String("signature", "", "detached signature file for --package")
	name := fs.String("name", "", "core name")
	version := fs.String("version", "0.1.0", "core version (semver)")
	publisher := fs.String("publisher", "self", "publisher id")
	sign := addSigningFlags(fs, "rollout set atomically with the install: auto | optin | off (default: gateway default, auto)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validRolloutFlag(*sign.rollout); err != nil {
		return err
	}
	pkgBody, err := buildPackage(*daemonURL, *manifestFile, *packageFile, *name, *version, *publisher)
	if err != nil {
		return err
	}
	if _, err := pkgmeta.ParsePackage(pkgBody); err != nil {
		return fmt.Errorf("package invalid: %w", err)
	}
	var signature string
	if *packageFile != "" && *signatureFile != "" {
		// A pre-built package keeps the signature its publisher produced.
		raw, err := os.ReadFile(expandHome(*signatureFile))
		if err != nil {
			return err
		}
		signature = string(raw)
	} else {
		priv, err := sign.key()
		if err != nil {
			return err
		}
		if signature, err = signPackageBody(pkgBody, priv); err != nil {
			return err
		}
	}
	c := gw.client()
	if err := publishComponent(c, pkgBody, signature, *sign.rollout); err != nil {
		return err
	}
	fmt.Println("installed")
	return nil
}

// publishComponent is the ONE component publish call (install, publish-sidecar, publish-mcp):
// POST /v1/platform/components with the package, its signature, and an
// optional rollout the gateway applies atomically with the publish (the same
// request the platform console sends). The package is held to the install
// gate (pkgmeta.ParsePackage) before it is sent. A notifier's first install
// answers with its signing secret: it is printed here, the only time it is
// shown.
func publishComponent(c *client, pkgBody []byte, signature, rollout string) error {
	if _, err := pkgmeta.ParsePackage(pkgBody); err != nil {
		return fmt.Errorf("package invalid: %w", err)
	}
	req := map[string]string{"package": string(pkgBody), "signature": signature}
	if rollout != "" {
		req["rollout"] = rollout
	}
	var out struct {
		Name   string `json:"name"`
		Secret string `json:"secret"`
	}
	if err := c.call(context.Background(), apiTimeout, http.MethodPost, "/v1/platform/components", req, &out); err != nil {
		return err
	}
	if out.Secret != "" {
		printSigningSecret(out.Name, out.Secret)
	}
	return nil
}

// printSigningSecret shows a notifier's signing secret, which is never
// readable again (rotate to get a new one).
func printSigningSecret(name, secret string) {
	fmt.Printf("signing secret for %s (shown once; configure it on the notifier endpoint):\n%s\n", name, secret)
}

// validRolloutFlag accepts "" (gateway default) or a rollout policy.
func validRolloutFlag(rollout string) error {
	if rollout != "" && !extpipe.ValidRollout(rollout) {
		return fmt.Errorf("--rollout must be auto, optin, or off, got %q", rollout)
	}
	return nil
}

// buildPackage assembles the package body from exactly one source: --package
// (pass through), --manifest (wrap a legacy REST manifest), or --url (probe a
// protocol-conforming daemon and synthesize an endpoint package — the zero-JSON
// path).
func buildPackage(daemonURL, manifestFile, packageFile, name, version, publisher string) ([]byte, error) {
	switch {
	case packageFile != "":
		return os.ReadFile(expandHome(packageFile))
	case manifestFile != "":
		raw, err := os.ReadFile(expandHome(manifestFile))
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, fmt.Errorf("--name is required with --manifest")
		}
		var m manifestcore.Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("manifest json invalid: %w", err)
		}
		pkg := pkgmeta.Package{
			Kind: pkgmeta.KindCorePackageV1, Name: name, Version: version,
			Runtime: pkgmeta.RuntimeManifest, Publisher: publisher,
			Protocol: protocol.ProtocolRange{Min: protocol.SupportedMinVersion, Max: protocol.SupportedMaxVersion},
			Manifest: &m,
		}
		return json.Marshal(pkg)
	case daemonURL != "":
		if name == "" {
			return nil, fmt.Errorf("--name is required with --url")
		}
		disc, host, err := probeDaemon(daemonURL)
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", daemonURL, err)
		}
		pkg := pkgmeta.Package{
			Kind: pkgmeta.KindCorePackageV1, Name: name, Version: version,
			Runtime:     pkgmeta.RuntimeEndpoint,
			Endpoint:    &pkgmeta.Endpoint{BaseURL: daemonURL},
			Protocol:    disc.Protocol,
			Permissions: pkgmeta.Permissions{Network: []string{host}},
			Publisher:   publisher,
		}
		return json.Marshal(pkg)
	default:
		return nil, fmt.Errorf("install: one of --url, --manifest, or --package is required")
	}
}

// probeDaemon fetches a daemon's Core Protocol v1 discovery so an endpoint
// package can carry the negotiated protocol range + network host.
func probeDaemon(daemonURL string) (protocol.Discovery, string, error) {
	u, err := url.Parse(daemonURL)
	if err != nil || u.Host == "" {
		return protocol.Discovery{}, "", fmt.Errorf("invalid --url")
	}
	daemon := &client{cfg: config{Gateway: strings.TrimRight(daemonURL, "/")}, http: defaultDaemonClient()}
	var disc protocol.Discovery
	if err := daemon.call(context.Background(), apiTimeout, http.MethodGet, protocol.PathDiscover, nil, &disc); err != nil {
		return protocol.Discovery{}, "", err
	}
	return disc, u.Hostname(), nil
}

func coresList(args []string) error {
	fs := flag.NewFlagSet("cores list", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := gw.client()
	var out struct {
		Cores []struct {
			Name      string `json:"name"`
			Available bool   `json:"available"`
			Builtin   bool   `json:"builtin"`
			Backend   string `json:"backend"`
		} `json:"cores"`
	}
	if err := c.call(context.Background(), apiTimeout, http.MethodGet, "/v1/cores", nil, &out); err != nil {
		return err
	}
	fmt.Printf("%-24s %-10s %-9s %s\n", "NAME", "BACKEND", "BUILTIN", "AVAILABLE")
	for _, c := range out.Cores {
		fmt.Printf("%-24s %-10s %-9v %v\n", c.Name, c.Backend, c.Builtin, c.Available)
	}
	return nil
}

func coresUninstall(args []string) error {
	fs := flag.NewFlagSet("cores uninstall", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("usage: cores uninstall <name>")
	}
	c := gw.client()
	if err := c.call(context.Background(), apiTimeout, http.MethodDelete, coreComponentPath(name, ""), nil, nil); err != nil {
		return err
	}
	fmt.Println("uninstalled")
	return nil
}

func coresEnable(args []string) error {
	fs := flag.NewFlagSet("cores enable", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	allTenants := fs.Bool("all-tenants", false, "enable for all tenants (clears the overlay)")
	var tenants multiFlag
	fs.Var(&tenants, "tenant", "tenant id to enable (repeatable)")
	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("usage: cores enable <name> (--tenant <id>... | --all-tenants)")
	}
	if *allTenants == (len(tenants) > 0) {
		// The tenant overlay is explicit and fail-closed: either every tenant
		// or a non-empty list, never both and never an empty list.
		return fmt.Errorf("cores enable: exactly one of --tenant <id>... or --all-tenants is required")
	}
	c := gw.client()
	req := map[string]any{"enabledTenants": []string(tenants), "allTenants": *allTenants}
	if err := c.call(context.Background(), apiTimeout, http.MethodPut, coreComponentPath(name, "/tenants"), req, nil); err != nil {
		return err
	}
	fmt.Println("updated")
	return nil
}

// coreComponentPath addresses a CORE component by name. ?kind=core pins the
// kind so a plugin that shares the name never makes the request ambiguous
// (the gateway answers 409 for an unqualified name matching several kinds).
func coreComponentPath(name, suffix string) string {
	return "/v1/platform/components/" + url.PathEscape(name) + suffix + "?kind=core"
}

// parseInterspersed parses fs over args allowing flags before, between, and
// after positionals (the standard flag package stops at the first positional).
// Each flag is parsed by fs itself, so a flag's value ("--gateway URL") is
// never mistaken for a positional. "--" ends flag parsing.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positionals, nil
		}
		if args[0] == "--" {
			return append(positionals, args[1:]...), nil
		}
		positionals = append(positionals, args[0])
		args = args[1:]
	}
}

// parseNamed parses a command taking exactly one positional (a component
// name) anywhere among its flags. Zero positionals return "" (the caller
// prints its usage); more than one is an error.
func parseNamed(fs *flag.FlagSet, args []string) (string, error) {
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return "", err
	}
	switch len(pos) {
	case 0:
		return "", nil
	case 1:
		return pos[0], nil
	default:
		return "", fmt.Errorf("%s: unexpected arguments %q", fs.Name(), pos[1:])
	}
}

// defaultSeckeyHint is the publisher key `bnbloc keys gen --out
// ~/.bnbloc/component-publisher` writes; signing commands use it when
// --seckey is not given and the file exists.
const defaultSeckeyHint = "~/.bnbloc/component-publisher.key"

// packageSigningKey loads the signing key before anything is uploaded: the
// --seckey file, else the default publisher key when it exists. --unsigned
// returns nil (publish without a signature; the gateway must allow it).
func packageSigningKey(seckeyFile string, unsigned bool) (ed25519.PrivateKey, error) {
	if unsigned {
		return nil, nil
	}
	path := expandHome(seckeyFile)
	if path == "" {
		def := expandHome(defaultSeckeyHint)
		if _, err := os.Stat(def); err != nil {
			return nil, fmt.Errorf("sign: no --seckey given and %s does not exist (create one with `bnbloc keys gen --out ~/.bnbloc/component-publisher`, or use --unsigned for a dev gateway)", defaultSeckeyHint)
		}
		path = def
	}
	priv, err := loadPrivateKey(path)
	if err != nil {
		return nil, fmt.Errorf("sign: %s: %w", path, err)
	}
	return priv, nil
}

// signPackageBody returns the detached signature of body, or "" for a nil key
// (an --unsigned publish).
func signPackageBody(body []byte, priv ed25519.PrivateKey) (string, error) {
	if priv == nil {
		return "", nil
	}
	sig, err := pkgmeta.SignPackage(body, priv)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	return string(sig), nil
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
