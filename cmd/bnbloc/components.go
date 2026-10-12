package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
)

// runComponents handles component operations that are not core-specific:
// notifier signing-secret rotation and test sends.
func runComponents(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("components: expected a subcommand (rotate-secret|test)")
	}
	switch args[0] {
	case "rotate-secret":
		return componentsRotateSecret(args[1:])
	case "test":
		return componentsTest(args[1:])
	default:
		return fmt.Errorf("components: unknown subcommand %q", args[0])
	}
}

// notifierComponentPath addresses a NOTIFIER component by name (?kind= pins
// the kind, as coreComponentPath does for cores).
func notifierComponentPath(name, suffix string) string {
	return "/v1/platform/components/" + url.PathEscape(name) + suffix + "?kind=notifier"
}

func componentsRotateSecret(args []string) error {
	fs := flag.NewFlagSet("components rotate-secret", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("usage: components rotate-secret <name>")
	}
	c := gw.client()
	var out struct {
		Secret string `json:"secret"`
	}
	if err := c.call(context.Background(), apiTimeout, http.MethodPost, notifierComponentPath(name, "/secret/rotate"), nil, &out); err != nil {
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
