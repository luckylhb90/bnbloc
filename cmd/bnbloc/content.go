package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Content components at platform scope (docs/prd/2026-10-12-experts-and-
// teams.md, step 4): a super admin promotes a member's or an
// organization's expert, skill pack or prompt pack to every organization
// through the same signed install executable components use. Signing
// stays here, offline:
//
//	bnbloc content export <id> [--version v] [--publisher p] [--out f]   the signable package
//	bnbloc cores install --package f --signature f.minisig               install it signed
//	bnbloc content promote <id> [--version v] [--publisher p] [--rollout auto|off] [--seckey f | --unsigned]

func runContent(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("content: expected a subcommand (export|promote)")
	}
	switch args[0] {
	case "export":
		return contentExport(args[1:])
	case "promote":
		return contentPromote(args[1:])
	default:
		return fmt.Errorf("content: unknown subcommand %q", args[0])
	}
}

// exportContentPackage fetches the signable package of one version of a
// content component (GET /v1/platform/content-components/{id}/package).
// The bytes are exactly what gets signed.
func exportContentPackage(c *client, id, version, publisher string) ([]byte, error) {
	q := url.Values{}
	if version != "" {
		q.Set("version", version)
	}
	if publisher != "" {
		q.Set("publisher", publisher)
	}
	path := "/v1/platform/content-components/" + url.PathEscape(id) + "/package"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var raw json.RawMessage
	if err := c.call(context.Background(), apiTimeout, http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func contentExport(args []string) error {
	fs := flag.NewFlagSet("content export", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	version := fs.String("version", "", "the version to export (default: the latest)")
	publisher := fs.String("publisher", "self", "publisher id written into the package")
	out := fs.String("out", "", "write the package to this file (default: print it)")
	id, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("content export: the component id (cc_...) is required")
	}
	body, err := exportContentPackage(gw.client(), id, *version, *publisher)
	if err != nil {
		return err
	}
	if *out == "" {
		_, err = os.Stdout.Write(body)
		return err
	}
	if err := os.WriteFile(expandHome(*out), body, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s; sign it offline and install it with:\n  bnbloc cores install --package %s --signature <signature file>\n", *out, *out)
	return nil
}

func contentPromote(args []string) error {
	fs := flag.NewFlagSet("content promote", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	version := fs.String("version", "", "the version to promote (default: the latest)")
	publisher := fs.String("publisher", "self", "publisher id written into the package")
	sign := addSigningFlags(fs, "auto (default) or off (installed unpublished)")
	id, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("content promote: the component id (cc_...) is required")
	}
	if r := strings.TrimSpace(*sign.rollout); r != "" && r != "auto" && r != "off" {
		return fmt.Errorf("--rollout must be auto or off for content (members turn content on themselves)")
	}
	// The key is loaded before anything is fetched or installed.
	priv, err := sign.key()
	if err != nil {
		return err
	}
	c := gw.client()
	body, err := exportContentPackage(c, id, *version, *publisher)
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
	fmt.Println("promoted to every organization")
	return nil
}
