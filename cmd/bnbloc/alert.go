package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
)

// `bnbloc alert` sends an operational alert to platform admins through the
// installed notifier components (POST /v1/platform/alerts, super-admin).

// adminAlert is the alert request (wire.AdminAlertRequest).
type adminAlert struct {
	Title      string   `json:"title"`
	Text       string   `json:"text,omitempty"`
	URL        string   `json:"url,omitempty"`
	Recipients []string `json:"recipients,omitempty"`
}

// adminAlertResult is the accepted alert (wire.AdminAlertResponse).
type adminAlertResult struct {
	ID         string   `json:"id"`
	Recipients []string `json:"recipients"`
	Notifiers  []string `json:"notifiers"`
}

func runAlert(args []string) error {
	fs := flag.NewFlagSet("alert", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "gateway base URL")
	token := fs.String("token", "", "session token")
	title := fs.String("title", "", "alert headline (required, at most 200 characters)")
	text := fs.String("text", "", "alert body (at most 500 characters)")
	link := fs.String("url", "", "link the alert points at (absolute http(s) URL)")
	to := fs.String("to", "", "comma-separated recipient member ids (default: every platform super-admin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("alert: unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*title) == "" {
		return errors.New("alert: --title is required")
	}
	c := newClient(resolveConfig(*gateway, *token))
	res, err := sendAlert(context.Background(), c, adminAlert{Title: *title, Text: *text, URL: *link, Recipients: splitList(*to)})
	if err != nil {
		return err
	}
	printAlertResult(res)
	return nil
}

// sendAlert posts one alert. The gateway answers 202 once it is queued;
// delivery is asynchronous (the notifier's outbox).
func sendAlert(ctx context.Context, c *client, a adminAlert) (adminAlertResult, error) {
	var res adminAlertResult
	if err := smokeCall(ctx, c, apiTimeout, http.MethodPost, "/v1/platform/alerts", a, &res); err != nil {
		return res, err
	}
	if res.ID == "" {
		return res, errors.New("alert: invalid response")
	}
	return res, nil
}

func printAlertResult(res adminAlertResult) {
	fmt.Printf("alert %s queued for %s via %s\n", res.ID, strings.Join(res.Recipients, ", "), strings.Join(res.Notifiers, ", "))
}

// splitList parses a comma-separated flag into trimmed, non-empty items.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// smokeAlert builds the alert for a failed smoke run: the gateway and every
// failed step (truncated to the alert text limit).
func smokeAlert(gateway string, failures []string, runErr error, agent string) adminAlert {
	lines := append([]string(nil), failures...)
	if len(lines) == 0 && runErr != nil {
		lines = append(lines, runErr.Error())
	}
	text := strings.Join(lines, "\n")
	if agent != "" {
		text = "agent " + agent + "\n" + text
	}
	if r := []rune(text); len(r) > 500 {
		text = string(r[:499]) + "…"
	}
	a := adminAlert{Title: truncateRunes("bnbloc smoke failed on "+gateway, 200), Text: text}
	if strings.HasPrefix(gateway, "https://") || strings.HasPrefix(gateway, "http://") {
		a.URL = gateway
	}
	return a
}
