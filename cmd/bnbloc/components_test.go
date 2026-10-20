package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn and returns what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// A notifier install prints its one-time signing secret; rotate-secret asks
// for the notifier kind explicitly and prints the new secret.
func TestNotifierSecretIsPrintedOnInstallAndRotate(t *testing.T) {
	isolateHome(t)
	var rotatePath string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/platform/components":
			_ = json.NewEncoder(w).Encode(map[string]string{"kind": "notifier", "name": "corp", "secret": "whsec_first"})
		case r.Method == "POST" && r.URL.Path == "/v1/platform/components/corp/secret/rotate":
			rotatePath = r.URL.RequestURI()
			_ = json.NewEncoder(w).Encode(map[string]string{"secret": "whsec_second"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gw.Close()
	pkg := writeTemp(t, "pkg.json", `{"kind":"bnbloc.notifier.package/v1","name":"corp","version":"1.0.0","runtime":"endpoint",`+
		`"contract":"bnbloc.notifier.contract/v1","endpoint":{"baseUrl":"https://notify.example.com"},`+
		`"permissions":{"network":["notify.example.com"]},"publisher":"corp","description":""}`)
	sig := writeTemp(t, "pkg.sig", "sig\n")
	out, err := captureStdout(t, func() error {
		return coresInstall([]string{"--gateway", gw.URL, "--token", "admin", "--package", pkg, "--signature", sig})
	})
	if err != nil || !strings.Contains(out, "whsec_first") {
		t.Fatalf("install output = %q err=%v", out, err)
	}
	out, err = captureStdout(t, func() error {
		return runComponents([]string{"rotate-secret", "corp", "--gateway", gw.URL, "--token", "admin"})
	})
	if err != nil || !strings.Contains(out, "whsec_second") || rotatePath != "/v1/platform/components/corp/secret/rotate?kind=notifier" {
		t.Fatalf("rotate output = %q err=%v path=%s", out, err, rotatePath)
	}
	// An auth provider's credential rotates the same way, by its kind.
	out, err = captureStdout(t, func() error {
		return runComponents([]string{"rotate-secret", "corp", "--kind", "auth-provider", "--gateway", gw.URL, "--token", "admin"})
	})
	if err != nil || !strings.Contains(out, "whsec_second") || rotatePath != "/v1/platform/components/corp/secret/rotate?kind=auth-provider" {
		t.Fatalf("auth provider rotate output = %q err=%v path=%s", out, err, rotatePath)
	}
}
