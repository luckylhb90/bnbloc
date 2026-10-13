package config

import (
	"strings"
	"testing"
)

func TestRequireInProductionOnlyEnforcesInProduction(t *testing.T) {
	t.Setenv("BNBLOC_TEST_SET", "x")
	t.Setenv("BNBLOC_TEST_UNSET", "")
	t.Setenv(EnvVar, "")
	if err := RequireInProduction("BNBLOC_TEST_UNSET"); err != nil {
		t.Fatalf("dev mode must stay permissive, got %v", err)
	}
	t.Setenv(EnvVar, "Production")
	if !Production() {
		t.Fatal("BNBLOC_ENV=Production must select production mode")
	}
	if err := RequireInProduction("BNBLOC_TEST_SET"); err != nil {
		t.Fatalf("set variable reported missing: %v", err)
	}
	err := RequireInProduction("BNBLOC_TEST_SET", "BNBLOC_TEST_UNSET", "BNBLOC_TEST_NEVER")
	if err == nil || !strings.Contains(err.Error(), "BNBLOC_TEST_UNSET, BNBLOC_TEST_NEVER") || strings.Contains(err.Error(), "BNBLOC_TEST_SET,") {
		t.Fatalf("missing list wrong: %v", err)
	}
}

// A dev-only switch refuses production startup only when it is on (or
// malformed): an explicit off is the same as unset
// (docs/bugs/2026-10-13-destructive-migration-broke-mixed-deploy.md).
func TestRefuseInProductionOnlyWhenOn(t *testing.T) {
	t.Setenv(EnvVar, "production")
	for raw, refused := range map[string]bool{
		"": false, "  ": false, "0": false, "false": false, "OFF": false, "no": false, "f": false,
		"1": true, "true": true, "on": true, "yes": true, "enabled": true,
	} {
		t.Setenv("BNBLOC_TEST_DEV_SWITCH", raw)
		if err := RefuseInProduction("BNBLOC_TEST_DEV_SWITCH"); (err != nil) != refused {
			t.Errorf("BNBLOC_TEST_DEV_SWITCH=%q: refused = %v, want %v", raw, err != nil, refused)
		}
	}
	t.Setenv(EnvVar, "")
	t.Setenv("BNBLOC_TEST_DEV_SWITCH", "1")
	if err := RefuseInProduction("BNBLOC_TEST_DEV_SWITCH"); err != nil {
		t.Fatalf("development refuses nothing: %v", err)
	}
}

func TestStrictBool(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "yes": true, "OFF": false, "t": true, "F": false} {
		t.Setenv("BNBLOC_TEST_BOOL", raw)
		if got, err := StrictBool("BNBLOC_TEST_BOOL"); err != nil || got != want {
			t.Errorf("StrictBool(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	t.Setenv("BNBLOC_TEST_BOOL", "maybe")
	if _, err := StrictBool("BNBLOC_TEST_BOOL"); err == nil || !strings.Contains(err.Error(), "BNBLOC_TEST_BOOL") {
		t.Fatalf("a malformed switch must refuse, naming it: %v", err)
	}
}
