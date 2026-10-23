package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// runComponents handles component operations that are not core-specific:
// an endpoint component's credential rotation, notifier test sends, and publishing a package
// file with its binaries.
func runComponents(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("components: expected a subcommand (list|rotate-secret|test|publish-package)")
	}
	switch args[0] {
	case "list":
		return componentsList(args[1:])
	case "rotate-secret":
		return componentsRotateSecret(args[1:])
	case "test":
		return componentsTest(args[1:])
	case "publish-package":
		return componentsPublishPackage(args[1:])
	default:
		return fmt.Errorf("components: unknown subcommand %q", args[0])
	}
}

// componentsList prints every installed component: kind, name, version
// and rollout (what a release reads before it publishes over one).
func componentsList(args []string) error {
	fs := flag.NewFlagSet("components list", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	var out wire.PlatformComponentsReply
	if err := gw.client().call(context.Background(), apiTimeout, http.MethodGet, "/v1/platform/components", nil, &out); err != nil {
		return err
	}
	fmt.Printf("%-14s %-28s %-12s %s\n", "KIND", "NAME", "VERSION", "ROLLOUT")
	for _, c := range out.Components {
		fmt.Printf("%-14s %-28s %-12s %s\n", c.Kind, c.Name, c.Version, c.Rollout)
	}
	return nil
}

// kindComponentPath addresses a component by kind and name (?kind= pins
// the kind, as coreComponentPath does for cores).
func kindComponentPath(kind, name, suffix string) string {
	return "/v1/platform/components/" + url.PathEscape(name) + suffix + "?kind=" + url.QueryEscape(kind)
}

// notifierComponentPath addresses a NOTIFIER component by name.
func notifierComponentPath(name, suffix string) string {
	return kindComponentPath("notifier", name, suffix)
}

func componentsRotateSecret(args []string) error {
	fs := flag.NewFlagSet("components rotate-secret", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	kind := fs.String("kind", "notifier", "the endpoint component's kind: notifier, auth-provider, share-target or service")
	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("usage: components rotate-secret <name> [--kind notifier|auth-provider|share-target|service]")
	}
	c := gw.client()
	var out struct {
		Secret string `json:"secret"`
	}
	if err := c.call(context.Background(), apiTimeout, http.MethodPost, kindComponentPath(*kind, name, "/secret/rotate"), nil, &out); err != nil {
		return err
	}
	if out.Secret == "" {
		return fmt.Errorf("components rotate-secret: the answer carries no secret")
	}
	printSigningSecret(name, out.Secret)
	return nil
}

func componentsTest(args []string) error {
	fs := flag.NewFlagSet("components test", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("usage: components test <name>")
	}
	c := gw.client()
	var out struct {
		Delivered bool   `json:"delivered"`
		Status    int    `json:"status"`
		Error     string `json:"error"`
		EventID   string `json:"eventId"`
	}
	if err := c.call(context.Background(), apiTimeout, http.MethodPost, notifierComponentPath(name, "/test"), nil, &out); err != nil {
		return err
	}
	if !out.Delivered {
		return fmt.Errorf("test notification %s failed (HTTP %d): %s", out.EventID, out.Status, out.Error)
	}
	fmt.Printf("test notification %s delivered (HTTP %d)\n", out.EventID, out.Status)
	return nil
}
