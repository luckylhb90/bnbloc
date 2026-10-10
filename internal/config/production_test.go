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
