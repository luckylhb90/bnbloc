package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// `bnbloc org-sync` drives organization sync (R11) over the super-admin
// /v1/platform/org-sync/* routes: the mapping rules, a dry-run preview,
// deprovisioning departed users (the call a directory feed makes), and a
// resync that re-applies changed rules to every member a sign-in recorded.

const orgSyncUsage = `usage:
  bnbloc org-sync rules                         print the mapping rules (JSON)
  bnbloc org-sync set-rules --file <rules.json> replace the rules ({"rules":[...]} or a bare list)
  bnbloc org-sync preview --email <e> [--group g]... [--department d] [--user id]
  bnbloc org-sync deprovision <userId>... | --file <ids.txt|->   one id per line
  bnbloc org-sync resync                        re-apply the rules to every recorded member`

func runOrgSync(args []string) error {
	if len(args) == 0 {
		return errors.New(orgSyncUsage)
	}
	fs := flag.NewFlagSet("org-sync "+args[0], flag.ContinueOnError)
	gateway := fs.String("gateway", "", "gateway base URL")
	token := fs.String("token", "", "session token")
	file := fs.String("file", "", "input file (- for stdin)")
	email := fs.String("email", "", "preview: the member's email")
	user := fs.String("user", "", "preview: the member's subject id (default: the email)")
	department := fs.String("department", "", "preview: the asserted department")
	var groups multiFlag
	fs.Var(&groups, "group", "preview: an asserted group (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx := context.Background()
	c := newClient(resolveConfig(*gateway, *token))
	switch args[0] {
	case "rules":
		var out json.RawMessage
		if err := smokeCall(ctx, c, apiTimeout, http.MethodGet, "/v1/platform/org-sync/rules", nil, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "set-rules":
		raw, err := readInput(*file)
		if err != nil {
			return err
		}
		body, err := rulesBody(raw)
		if err != nil {
			return err
		}
		var out json.RawMessage
		if err := smokeCall(ctx, c, apiTimeout, http.MethodPut, "/v1/platform/org-sync/rules", body, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "preview":
		if strings.TrimSpace(*email) == "" && strings.TrimSpace(*user) == "" {
			return errors.New("org-sync preview: --email or --user is required")
		}
		var out json.RawMessage
		in := map[string]any{"userId": *user, "email": *email, "groups": []string(groups), "department": *department}
		if err := smokeCall(ctx, c, apiTimeout, http.MethodPost, "/v1/platform/org-sync/preview", in, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "deprovision":
		ids := fs.Args()
		if *file != "" {
			raw, err := readInput(*file)
			if err != nil {
				return err
			}
			ids = append(ids, lines(raw)...)
		}
		if len(ids) == 0 {
			return errors.New("org-sync deprovision: name at least one user id")
		}
		var out struct {
			Results []struct {
				UserID          string   `json:"userId"`
				RemovedTenants  []string `json:"removedTenants"`
				RevokedSessions int      `json:"revokedSessions"`
			} `json:"results"`
		}
		if err := smokeCall(ctx, c, apiTimeout, http.MethodPost, "/v1/platform/org-sync/deprovision", map[string]any{"userIds": ids}, &out); err != nil {
			return err
		}
		for _, r := range out.Results {
			fmt.Printf("%s: removed from %d tenant(s) [%s], revoked %d session(s)\n",
				r.UserID, len(r.RemovedTenants), strings.Join(r.RemovedTenants, ", "), r.RevokedSessions)
		}
		return nil
	case "resync":
		var out json.RawMessage
		if err := smokeCall(ctx, c, apiTimeout, http.MethodPost, "/v1/platform/org-sync/resync", map[string]any{}, &out); err != nil {
			return err
		}
		return printJSON(out)
	default:
		return fmt.Errorf("org-sync: unknown subcommand %q\n%s", args[0], orgSyncUsage)
	}
}

// rulesBody accepts {"rules":[...]} or a bare rule list.
func rulesBody(raw []byte) (map[string]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		return map[string]json.RawMessage{"rules": json.RawMessage(trimmed)}, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body["rules"] == nil {
		return nil, errors.New(`org-sync set-rules: the file must hold {"rules":[...]} or a JSON list of rules`)
	}
	return map[string]json.RawMessage{"rules": body["rules"]}, nil
}

func readInput(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("org-sync: --file is required")
	}
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// lines returns the non-empty, non-comment lines of raw.
func lines(raw []byte) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func printJSON(raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
