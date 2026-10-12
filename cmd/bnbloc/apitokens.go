package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// API tokens: a super-admin issues a long-lived, revocable credential for an
// unattended caller (the nightly smoke workflow). The CLI reads it from
// BNBLOC_API_TOKEN and exchanges it once per process for a 1-hour session.

// apiTokenEnv names the API token environment variable.
const apiTokenEnv = "BNBLOC_API_TOKEN"

// exchangeAPIToken trades the API token for a session (POST /v1/auth/api-token).
func (c *client) exchangeAPIToken() (*sessionGrant, error) {
	body, _ := json.Marshal(map[string]string{"token": c.cfg.APIToken})
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	raw, status, err := c.send(ctx, http.MethodPost, "/v1/auth/api-token", bytes.NewReader(body), "application/json", "")
	if err != nil {
		return nil, fmt.Errorf("exchange %s: %w", apiTokenEnv, err)
	}
	if status == http.StatusUnauthorized || status == http.StatusBadRequest {
		return nil, fmt.Errorf("%s was refused by %s (invalid, expired or revoked); issue a new one with `bnbloc api-tokens create`", apiTokenEnv, c.cfg.Gateway)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("exchange %s: %d: %s", apiTokenEnv, status, strings.TrimSpace(string(raw)))
	}
	var g sessionGrant
	if err := json.Unmarshal(raw, &g); err != nil || g.SessionToken == "" || g.Subject == "" {
		return nil, fmt.Errorf("exchange %s: invalid response", apiTokenEnv)
	}
	return &g, nil
}

// apiTokenRow is one row of GET /v1/platform/api-tokens (and the create
// answer, which adds the token once).
type apiTokenRow struct {
	ID           string `json:"id"`
	Subject      string `json:"subject"`
	Label        string `json:"label"`
	Tenant       string `json:"tenant"`
	CreatedBy    string `json:"createdBy"`
	CreatedAtMs  int64  `json:"createdAtMs"`
	ExpiresAtMs  int64  `json:"expiresAtMs"`
	LastUsedAtMs int64  `json:"lastUsedAtMs"`
	RevokedAtMs  int64  `json:"revokedAtMs"`
	Active       bool   `json:"active"`
	Token        string `json:"token,omitempty"`
}

func runAPITokens(args []string) error {
	if len(args) == 0 {
		return errors.New("api-tokens: want create | list | revoke <id>")
	}
	fs := flag.NewFlagSet("api-tokens "+args[0], flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	label := fs.String("label", "", "what the token is for (create)")
	subject := fs.String("subject", "", "svc:<name> service subject (create; default: yourself)")
	ttlDays := fs.Int("ttl-days", 0, "lifetime in days, 1-365 (create; default 90)")
	tenantID := fs.String("tenant", "", "tenant the token's sessions resolve in (create; default: your session's)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c := gw.client()
	switch args[0] {
	case "create":
		if fs.NArg() > 0 {
			return fmt.Errorf("api-tokens create: unexpected argument %q", fs.Arg(0))
		}
		if strings.TrimSpace(*label) == "" {
			return errors.New("api-tokens create: --label is required")
		}
		var row apiTokenRow
		in := map[string]any{"label": *label, "subject": *subject, "ttlDays": *ttlDays, "tenantId": *tenantID}
		if err := c.call(context.Background(), apiTimeout, http.MethodPost, "/v1/platform/api-tokens", in, &row); err != nil {
			return err
		}
		if row.Token == "" {
			return errors.New("api-tokens create: the answer carries no token")
		}
		fmt.Printf("id       %s\nsubject  %s\nexpires  %s\n\n%s\n\nThe token is shown once. Store it as a secret (%s); revoke with `bnbloc api-tokens revoke %s`.\n",
			row.ID, row.Subject, msTime(row.ExpiresAtMs), row.Token, apiTokenEnv, row.ID)
		return nil
	case "list":
		var out struct {
			Tokens []apiTokenRow `json:"tokens"`
		}
		if err := c.call(context.Background(), apiTimeout, http.MethodGet, "/v1/platform/api-tokens", nil, &out); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSUBJECT\tLABEL\tSTATE\tEXPIRES\tLAST USED\tCREATED BY")
		for _, t := range out.Tokens {
			state := "active"
			switch {
			case t.RevokedAtMs != 0:
				state = "revoked"
			case !t.Active:
				state = "expired"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Subject, t.Label, state, msTime(t.ExpiresAtMs), msTime(t.LastUsedAtMs), t.CreatedBy)
		}
		return tw.Flush()
	case "revoke":
		if fs.NArg() != 1 {
			return errors.New("api-tokens revoke: want exactly one token id")
		}
		if err := c.call(context.Background(), apiTimeout, http.MethodDelete, "/v1/platform/api-tokens/"+url.PathEscape(fs.Arg(0)), nil, nil); err != nil {
			return err
		}
		fmt.Printf("revoked %s\n", fs.Arg(0))
		return nil
	}
	return fmt.Errorf("api-tokens: unknown subcommand %q (want create | list | revoke)", args[0])
}

// msTime renders a unix-ms timestamp ("-" for 0).
func msTime(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04Z")
}
