package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The one JSON call returns every failure: a status (401 with the sign-in
// hint, signed in or not), and an answer that does not decode.
func TestCallReturnsEveryFailure(t *testing.T) {
	isolateHome(t)
	t.Setenv(apiTokenEnv, "")
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/unauthorized":
			http.Error(w, "no", http.StatusUnauthorized)
		case "/forbidden":
			http.Error(w, "no", http.StatusForbidden)
		case "/garbage":
			_, _ = w.Write([]byte("<html>"))
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "x"})
		}
	}))
	t.Cleanup(gw.Close)
	ctx := context.Background()
	var out struct{ Name string }
	for _, token := range []string{"", testSession("maya", nil)} {
		err := newClient(resolveConfig(gw.URL, token)).call(ctx, apiTimeout, http.MethodGet, "/unauthorized", nil, &out)
		if err == nil || !strings.Contains(err.Error(), "bnbloc login") {
			t.Errorf("401 (token %v): err = %v, want the sign-in hint", token != "", err)
		}
	}
	c := newClient(resolveConfig(gw.URL, testSession("maya", nil)))
	var se *statusError
	if err := c.call(ctx, apiTimeout, http.MethodGet, "/forbidden", nil, &out); !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Errorf("403: err = %v", err)
	}
	if err := c.call(ctx, apiTimeout, http.MethodGet, "/garbage", nil, &out); err == nil || !strings.Contains(err.Error(), "invalid response") {
		t.Errorf("an undecodable answer: err = %v", err)
	}
	if err := c.call(ctx, apiTimeout, http.MethodDelete, "/empty", nil, &out); err != nil {
		t.Errorf("204: %v", err)
	}
	if err := c.call(ctx, apiTimeout, http.MethodGet, "/ok", map[string]any{"bad": make(chan int)}, &out); err == nil {
		t.Error("an unencodable request was sent")
	}
	if err := c.call(ctx, apiTimeout, http.MethodGet, "/ok", nil, &out); err != nil || out.Name != "x" {
		t.Errorf("ok = %+v, %v", out, err)
	}
}

// whoami reports the session every other command uses: with an API token
// in the environment that is the token's session, not the stored sign-in.
func TestWhoamiReportsTheSessionCommandsUse(t *testing.T) {
	home := isolateHome(t)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/api-token":
			_ = json.NewEncoder(w).Encode(map[string]string{"sessionToken": "st-api", "subject": "ci-bot", "tenant": "acme", "role": "member"})
		case "/v1/auth/refresh":
			_ = json.NewEncoder(w).Encode(map[string]string{"sessionToken": "st-2", "refreshToken": "rt-2", "subject": "maya", "tenant": "acme", "role": "org_admin"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gw.Close)
	writeConfigFile(t, home, map[string]any{"gateway": gw.URL, "refreshToken": "rt-1", "subject": "maya"})
	whoami := func(args ...string) string {
		t.Helper()
		out, err := captureStdout(t, func() error { return runWhoami(args) })
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	t.Setenv(apiTokenEnv, "bnbloc_at_x")
	if out := whoami(); !strings.Contains(out, "subject  ci-bot") || !strings.Contains(out, apiTokenEnv) {
		t.Fatalf("with %s: %s", apiTokenEnv, out)
	}
	if out := whoami("--token", testSession("jack", map[string]any{"tenant": "nimbus"})); !strings.Contains(out, "subject  jack") || !strings.Contains(out, "tenant   nimbus") {
		t.Fatalf("with --token: %s", out)
	}
	t.Setenv(apiTokenEnv, "")
	if out := whoami(); !strings.Contains(out, "subject  maya") || !strings.Contains(out, "stored sign-in") {
		t.Fatalf("stored sign-in: %s", out)
	}
}

// The CLI has one JSON call helper (client.call) and one gateway flag
// pair: no second helper, no hand-declared --gateway/--token outside
// flags.go (login takes --gateway alone).
func TestOneCallHelperAndFlagSet(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []struct {
		re   *regexp.Regexp
		what string
	}{
		{regexp.MustCompile(`func smokeCall|\bc\.do\(`), "a second JSON call helper (use client.call)"},
		{regexp.MustCompile(`fs\.String\("token"`), "a hand-declared --token (use addGatewayFlags)"},
		{regexp.MustCompile(`fs\.String\("seckey"`), "a hand-declared --seckey (use addSigningFlags)"},
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "flags.go" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range forbidden {
			if rule.re.Match(raw) {
				t.Errorf("%s has %s", f, rule.what)
			}
		}
	}
}
