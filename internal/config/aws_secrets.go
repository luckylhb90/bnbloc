// AWS Secrets Manager loading for service configuration.
//
// AWS Secrets Manager is a pure storage API: nothing maps it into a
// container's environment automatically. bnbloc loads it at process start,
// from this package — the single composition point every binary already
// imports — so the secret's values exist only in process memory and never
// touch the deployment's disk (unlike a deploy-time export to .env).
//
// Enable with BNBLOC_AWS_SECRET_ID=<name-or-arn> (optionally
// BNBLOC_AWS_REGION to pin the region). The secret's SecretString must be a
// JSON object mapping environment variable names to values, e.g.
//
//	{"BNBLOC_MYSQL_DSN":"user:pw@tcp(host:3306)/bnbloc",
//	 "BNBLOC_INTERNAL_KEY":"..."}
//
// which is exactly what the console's key-value editor produces. Values are
// set into the process environment only for keys whose current value is
// absent or EMPTY: a non-empty explicitly configured variable (compose,
// docker) always wins, so a secret can never silently override an operator's
// explicit override — but compose's ${VAR:-} passthroughs inject empty
// strings for every variable moved into the secret, and those must not
// shadow the secret's values. Unset = the feature is off and no AWS call is
// made.
//
// Credentials and region resolve through the AWS SDK default chain
// (instance profile on EC2, IRSA on EKS, access keys in the environment,
// shared config); a deployment on AWS needs no extra wiring beyond an IAM
// policy granting secretsmanager:GetSecretValue. A configured-but-unloadable
// secret is fatal — a service must never boot with an unknown storage
// backend (see the in-memory trap this repo already burned on once).
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// SecretIDEnv enables the loader: the AWS Secrets Manager secret name or ARN
// whose JSON keys become environment variables.
const SecretIDEnv = "BNBLOC_AWS_SECRET_ID"

// RegionEnv optionally pins the AWS region; unset defers to the SDK's default
// region chain (AWS_REGION, shared config, EC2 instance metadata).
const RegionEnv = "BNBLOC_AWS_REGION"

const secretLoadTimeout = 15 * time.Second

// bootLoadSecrets is the init entrypoint. Every outcome is observable:
// loading (start+result lines), disabled with a reason (set-but-empty — the
// signature of an --env-file/compose passthrough that never reached the
// container), or fatal (configured but unloadable). Only a fully unset
// variable (plain dev compose, tests, the desktop bridge) stays silent.
func bootLoadSecrets() {
	raw, set := os.LookupEnv(SecretIDEnv)
	secretID := strings.TrimSpace(raw)
	if secretID == "" {
		if set {
			log.Printf("bnbloc: config: %s is set but EMPTY — AWS Secrets Manager loading is disabled; configuration comes from the process environment only. If the secret id should be here, check --env-file and the compose passthrough; if it was a typo in the secret payload, values will also be missing (see the storage boot log).", SecretIDEnv)
		}
		return
	}
	if err := loadSecretEnv(context.Background(), secretID); err != nil {
		log.Fatalf("bnbloc: config: %s=%q set but the secret could not be loaded (check the instance role/credentials, AWS_REGION, and network egress to secretsmanager): %v", SecretIDEnv, secretID, err)
	}
}

func init() { bootLoadSecrets() }

// loadSecretEnv fetches the secret through the default AWS chain and applies
// it to the process environment.
func loadSecretEnv(ctx context.Context, secretID string) error {
	ctx, cancel := context.WithTimeout(ctx, secretLoadTimeout)
	defer cancel()
	region := strings.TrimSpace(os.Getenv(RegionEnv))
	regionFrom := "SDK default chain"
	if region != "" {
		regionFrom = region
	}
	log.Printf("bnbloc: config: loading service config from AWS Secrets Manager %s (region: %s, timeout %s)", secretID, regionFrom, secretLoadTimeout)
	cfg, err := awsconfig.LoadDefaultConfig(ctx, regionOpts(region)...)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	client := secretsmanager.NewFromConfig(cfg)
	return loadSecretFromClient(ctx, client, secretID)
}

func regionOpts(region string) []func(*awsconfig.LoadOptions) error {
	if region == "" {
		return nil
	}
	return []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
}

// loadSecretFromClient is the testable core: fetch, parse, overlay, and log.
func loadSecretFromClient(ctx context.Context, client getSecretValue, secretID string) error {
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretID)})
	if err != nil {
		return fmt.Errorf("fetch secret: %w", err)
	}
	if out.SecretString == nil {
		return fmt.Errorf("secret %q has no SecretString (binary secrets cannot map to environment variables)", secretID)
	}
	kv, err := parseSecretJSON(*out.SecretString)
	if err != nil {
		return fmt.Errorf("secret %q: %w (expected a JSON object of environment variable names to values, as the console's key-value editor produces)", secretID, err)
	}
	applied, skipped := applyEnvOverlay(kv)
	if len(applied) == 0 && len(skipped) == 0 {
		log.Printf("bnbloc: config: AWS secret %q contains no keys; nothing loaded", secretID)
		return nil
	}
	if len(applied) > 0 {
		log.Printf("bnbloc: config: loaded %d config values from AWS Secrets Manager %s: %s", len(applied), secretID, strings.Join(applied, ", "))
	}
	for _, k := range skipped {
		// Name every key the secret did NOT win: an operator comparing the
		// secret's contents against the boot log needs to see which values
		// came from the explicit environment instead.
		log.Printf("bnbloc: config: %s is already set to a non-empty value in the environment — the secret's value for it was NOT applied (explicit env wins)", k)
	}
	return nil
}

// getSecretValue is the one SDK surface the loader needs; implemented by
// *secretsmanager.Client and by the test fake.
type getSecretValue interface {
	GetSecretValue(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// parseSecretJSON decodes a secret's SecretString as env-name → value. Every
// value must be a string: environment variables cannot carry anything else.
func parseSecretJSON(raw string) (map[string]string, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("key %q: value is %T, want a string", k, v)
		}
		out[k] = s
	}
	return out, nil
}

func applyEnvOverlay(kv map[string]string) (applied, skipped []string) {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, exists := os.LookupEnv(k); exists && v != "" {
			skipped = append(skipped, k)
			continue
		}
		if os.Setenv(k, kv[k]) != nil {
			continue
		}
		applied = append(applied, k)
	}
	return applied, skipped
}
