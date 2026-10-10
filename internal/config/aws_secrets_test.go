package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

func TestParseSecretJSON_acceptsConsoleKeyValueSecret(t *testing.T) {
	raw := `{"BNBLOC_MYSQL_DSN":"u:p@tcp(h:3306)/bnbloc","BNBLOC_INTERNAL_KEY":"k"}`

	kv, err := parseSecretJSON(raw)

	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if kv["BNBLOC_MYSQL_DSN"] != "u:p@tcp(h:3306)/bnbloc" || kv["BNBLOC_INTERNAL_KEY"] != "k" {
		t.Fatalf("kv = %v", kv)
	}
}

func TestParseSecretJSON_rejectsNonObjectAndNonStringValues(t *testing.T) {
	for name, raw := range map[string]string{
		"array":            `["BNBLOC_MYSQL_DSN"]`,
		"not json":         `password123`,
		"non-string value": `{"BNBLOC_MYSQL_DSN":3306}`,
	} {
		if _, err := parseSecretJSON(raw); err == nil {
			t.Errorf("%s: parse(%q) succeeded, want error", name, raw)
		}
	}
}

// Explicit environment must win over the secret, only missing keys are
// applied — an operator's compose override can never be silently replaced —
// and the skipped key is REPORTED so the boot log shows which source won.
func TestApplyEnvOverlay_explicitEnvWinsAndReportsSkipped(t *testing.T) {
	t.Setenv("BNBLOC_EXPLICIT", "from-env")

	applied, skipped := applyEnvOverlay(map[string]string{
		"BNBLOC_EXPLICIT":    "from-secret",
		"BNBLOC_FROM_SECRET": "v",
	})

	if got := os.Getenv("BNBLOC_EXPLICIT"); got != "from-env" {
		t.Fatalf("explicit env overridden by secret: %q", got)
	}
	if got := os.Getenv("BNBLOC_FROM_SECRET"); got != "v" {
		t.Fatalf("secret value not applied: %q", got)
	}
	if len(applied) != 1 || applied[0] != "BNBLOC_FROM_SECRET" {
		t.Fatalf("applied = %v, want only the unset key", applied)
	}
	if len(skipped) != 1 || skipped[0] != "BNBLOC_EXPLICIT" {
		t.Fatalf("skipped = %v, want the already-set key reported", skipped)
	}
}

func TestApplyEnvOverlay_emptyEnvDoesNotShadowSecret(t *testing.T) {
	t.Setenv("BNBLOC_AUTH_PROVIDER_METADATA_URL", "")
	t.Setenv("BNBLOC_MYSQL_DSN", "")

	applied, skipped := applyEnvOverlay(map[string]string{
		"BNBLOC_AUTH_PROVIDER_METADATA_URL": "https://idp.example/metadata",
		"BNBLOC_MYSQL_DSN":                  "u:p@tcp(h:3306)/db",
	})

	if got := os.Getenv("BNBLOC_AUTH_PROVIDER_METADATA_URL"); got != "https://idp.example/metadata" {
		t.Fatalf("secret value shadowed by an empty env var: %q", got)
	}
	if got := os.Getenv("BNBLOC_MYSQL_DSN"); got != "u:p@tcp(h:3306)/db" {
		t.Fatalf("secret value shadowed by an empty env var: %q", got)
	}
	if len(applied) != 2 || len(skipped) != 0 {
		t.Fatalf("applied=%v skipped=%v, want all keys applied", applied, skipped)
	}
}

// captureLog runs fn with the standard logger captured and returns what it
// printed.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	fn()
	return buf.String()
}

// The set-but-empty state is how a secret id that never reached the container
// looks (missing --env-file, broken compose passthrough): it must say so,
// because it silently disables the whole loader.
func TestBootLoadSecrets_logsWhenSecretIDSetButEmpty(t *testing.T) {
	t.Setenv(SecretIDEnv, "")

	out := captureLog(t, bootLoadSecrets)

	if !strings.Contains(out, "set but EMPTY") || !strings.Contains(out, SecretIDEnv) {
		t.Fatalf("log = %q, want the disabled-with-reason line", out)
	}
}

// A fully unset variable (dev compose, tests, the desktop bridge) is silent:
// the feature is off by design there.
func TestBootLoadSecrets_silentWhenSecretIDUnset(t *testing.T) {
	t.Setenv(SecretIDEnv, "placeholder-to-allow-unset")
	os.Unsetenv(SecretIDEnv)

	out := captureLog(t, bootLoadSecrets)

	if strings.TrimSpace(out) != "" {
		t.Fatalf("log = %q, want silence for an unset variable", out)
	}
}

// fakeSecretsServer answers the SDK's GetSecretValue POST with a chosen
// SecretString, so the client path is exercised over a real HTTP wire.
func fakeSecretsServer(t *testing.T, secretString string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "secret") && r.Header.Get("X-Amz-Target") == "" {
			// The SDK posts to "/" with the operation in X-Amz-Target; anything
			// else here is unexpected but answered the same way.
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"SecretString": secretString})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testClient(t *testing.T, endpoint string) *secretsmanager.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	return secretsmanager.NewFromConfig(cfg, func(o *secretsmanager.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
}

func TestLoadSecretFromClient_overlaysEnvironment(t *testing.T) {
	srv := fakeSecretsServer(t, `{"BNBLOC_TEST_DSN":"u:p@tcp(h:3306)/db","BNBLOC_TEST_KEY":"k"}`)

	err := loadSecretFromClient(context.Background(), testClient(t, srv.URL), "prod/bnbloc")

	if err != nil {
		t.Fatalf("loadSecretFromClient: %v", err)
	}
	if got := os.Getenv("BNBLOC_TEST_DSN"); got != "u:p@tcp(h:3306)/db" {
		t.Fatalf("BNBLOC_TEST_DSN = %q, want the secret value", got)
	}
	if got := os.Getenv("BNBLOC_TEST_KEY"); got != "k" {
		t.Fatalf("BNBLOC_TEST_KEY = %q", got)
	}
}

func TestLoadSecretFromClient_rejectsBinarySecretWithActionableError(t *testing.T) {
	srv := fakeSecretsServer(t, "") // no SecretString in the response payload

	err := loadSecretFromClient(context.Background(), testClient(t, srv.URL), "prod/bnbloc")

	if err == nil {
		t.Fatal("empty SecretString accepted, want error")
	}
	if !strings.Contains(err.Error(), "JSON object") && !strings.Contains(err.Error(), "SecretString") {
		t.Fatalf("error not actionable: %v", err)
	}
}
