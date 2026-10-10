package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// CLI sign-in (`bnbloc login`) runs the gateway's SSO in the system browser.
// The CLI generates a 256-bit nonce, starts SSO with
// redirect_uri=<gateway>/sso-callback?cli_nonce=<nonce>, and polls
// POST /v1/auth/sso/claim with the nonce until the gateway hands over the
// grant. The browser never carries a token or code back (it lands on
// #cliSignedIn=1). Only the refresh token is stored; every command trades it
// for a short-lived session.

// Poll cadence of the claim loop (variables so tests can shorten them).
var (
	claimPollInterval = 2 * time.Second
	claimPollTimeout  = 5 * time.Minute
	openBrowser       = openInBrowser
)

func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "gateway base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := resolveConfig(*gateway, "")
	if cfg.configFile == "" {
		return errors.New("login: cannot locate the home directory for ~/.bnbloc/config.json")
	}
	c := newClient(config{Gateway: cfg.Gateway}) // unauthenticated
	grant, err := ssoLogin(c, os.Stdout)
	if err != nil {
		return err
	}
	if err := writeStoredLogin(cfg.configFile, storedLogin{Gateway: cfg.Gateway, RefreshToken: grant.RefreshToken, Subject: grant.Subject}); err != nil {
		return fmt.Errorf("store sign-in: %w", err)
	}
	fmt.Printf("signed in to %s as %s (role %s); saved to %s\n", cfg.Gateway, grant.Subject, roleOrNone(grant.Role), cfg.configFile)
	return nil
}

// ssoLogin runs the browser sign-in against c's gateway and returns the grant.
func ssoLogin(c *client, out io.Writer) (*sessionGrant, error) {
	body, err := c.do(http.MethodGet, "/v1/auth/sso-config", nil)
	if err != nil {
		return nil, fmt.Errorf("read sign-in options: %w", err)
	}
	var ssoCfg struct {
		AuthProviderAvailable bool   `json:"authProviderAvailable"`
		AuthProviderStartPath string `json:"authProviderStartPath"`
	}
	if err := json.Unmarshal(body, &ssoCfg); err != nil {
		return nil, fmt.Errorf("sign-in options invalid: %w", err)
	}
	if !ssoCfg.AuthProviderAvailable || strings.TrimSpace(ssoCfg.AuthProviderStartPath) == "" {
		return nil, fmt.Errorf("%s has no SSO sign-in configured; use --token / BNBLOC_TOKEN", c.cfg.Gateway)
	}
	nonce, err := newCLINonce()
	if err != nil {
		return nil, err
	}
	startURL := cliSSOStartURL(c.cfg.Gateway, ssoCfg.AuthProviderStartPath, nonce)
	fmt.Fprintf(out, "Opening your browser to sign in. If it does not open, visit:\n\n  %s\n\nWaiting for sign-in (up to %s)…\n", startURL, claimPollTimeout)
	if err := openBrowser(startURL); err != nil {
		fmt.Fprintf(out, "(could not open a browser: %v)\n", err)
	}
	return pollSSOClaim(c, nonce)
}

// cliSSOStartURL is <gateway><startPath>?redirect_uri=<gateway>/sso-callback?cli_nonce=<nonce>.
func cliSSOStartURL(gateway, startPath, nonce string) string {
	redirect := gateway + "/sso-callback?" + url.Values{"cli_nonce": {nonce}}.Encode()
	sep := "?"
	if strings.Contains(startPath, "?") {
		sep = "&"
	}
	return gateway + startPath + sep + url.Values{"redirect_uri": {redirect}}.Encode()
}

// newCLINonce returns 256 random bits, base64url (43 characters).
func newCLINonce() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// pollSSOClaim polls the claim endpoint until the grant arrives, a non-404
// refusal ends the flow, or claimPollTimeout passes. Transport errors are
// retried (the network may blip while the user signs in).
func pollSSOClaim(c *client, nonce string) (*sessionGrant, error) {
	body, _ := json.Marshal(map[string]string{"nonce": nonce})
	deadline := time.Now().Add(claimPollTimeout)
	var lastErr error
	for {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		raw, status, err := c.send(ctx, http.MethodPost, "/v1/auth/sso/claim", bytes.NewReader(body), "application/json", "")
		cancel()
		switch {
		case err != nil:
			lastErr = err
		case status == http.StatusOK:
			var g sessionGrant
			if err := json.Unmarshal(raw, &g); err != nil || g.RefreshToken == "" || g.Subject == "" {
				return nil, errors.New("sign-in claim returned an invalid grant")
			}
			return &g, nil
		case status == http.StatusNotFound:
			lastErr = nil // not signed in yet
		default:
			return nil, fmt.Errorf("sign-in claim refused: %d: %s", status, strings.TrimSpace(string(raw)))
		}
		if time.Now().Add(claimPollInterval).After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("sign-in not completed within %s (last error: %v)", claimPollTimeout, lastErr)
			}
			return nil, fmt.Errorf("sign-in not completed within %s; run bnbloc login again", claimPollTimeout)
		}
		time.Sleep(claimPollInterval)
	}
}

// openInBrowser opens u with the platform's URL opener.
func openInBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	default:
		return fmt.Errorf("no browser opener for %s", runtime.GOOS)
	}
	return cmd.Start()
}

func runLogout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	stored, err := readStoredLogin(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && stored.RefreshToken == "") {
		fmt.Println("not signed in")
		return nil
	} else if err != nil {
		return err
	}
	// Revoke the sign-in server-side first (best-effort: an unreachable
	// gateway must not keep the credential on disk), then keep the gateway
	// (the default for the next login) and drop the credential.
	if err := revokeStoredLogin(stored); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not revoke the sign-in on %s (%v); removing it locally\n", stored.Gateway, err)
	}
	if err := writeStoredLogin(path, storedLogin{Gateway: stored.Gateway}); err != nil {
		return err
	}
	fmt.Printf("signed out of %s\n", stored.Gateway)
	return nil
}

// logoutTimeout bounds the best-effort server-side revocation at logout.
const logoutTimeout = 5 * time.Second

// revokeStoredLogin revokes the stored refresh token's family on its gateway
// (POST /v1/auth/logout, idempotent).
func revokeStoredLogin(stored storedLogin) error {
	body, _ := json.Marshal(map[string]string{"refreshToken": stored.RefreshToken})
	ctx, cancel := context.WithTimeout(context.Background(), logoutTimeout)
	defer cancel()
	c := newClient(config{Gateway: normalizeGateway(stored.Gateway)})
	raw, status, err := c.send(ctx, http.MethodPost, "/v1/auth/logout", bytes.NewReader(body), "application/json", "")
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return fmt.Errorf("%d: %s", status, strings.TrimSpace(string(raw)))
	}
	return nil
}

func runWhoami(args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "gateway base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := resolveConfig(*gateway, "")
	if os.Getenv("BNBLOC_TOKEN") != "" {
		fmt.Fprintln(os.Stderr, "note: BNBLOC_TOKEN is set and takes precedence for other commands; whoami reports the stored sign-in")
	}
	cfg.Token = ""
	if cfg.RefreshToken == "" {
		return errNotSignedIn(cfg.Gateway, "not signed in to "+cfg.Gateway)
	}
	c := newClient(cfg)
	if _, err := c.bearer(); err != nil {
		return err
	}
	fmt.Printf("gateway  %s\nsubject  %s\ntenant   %s\nrole     %s\n", cfg.Gateway, c.grant.Subject, c.grant.Tenant, roleOrNone(c.grant.Role))
	return nil
}

func roleOrNone(role string) string {
	if role == "" {
		return "(none)"
	}
	return role
}
