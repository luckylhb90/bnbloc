package coreset

import (
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// EntryFor gives an installed core and an env-configured core the same
// entry: the BNBLOC_CORE_<NAME>_URL/_TOKEN override wins, a declared package
// address is fixed configuration, and only otherwise does discovery run.
func TestEntryForAppliesEnvOverridesToEveryCore(t *testing.T) {
	clearCoreEnv(t)
	p := protocol.NewClient("acme", nil)

	if e := EntryFor("acme", p, core.Endpoint{}); e.Explicit || e.Endpoint.OK() {
		t.Fatalf("no override, nothing declared, no discovery: got %+v", e)
	}
	declared := core.Endpoint{BaseURL: "https://agents.example"}
	if e := EntryFor("acme", p, declared); !e.Explicit || e.Endpoint != declared {
		t.Fatalf("declared endpoint: got %+v", e)
	}
	t.Setenv("BNBLOC_CORE_ACME_TOKEN", "tok")
	if e := EntryFor("acme", p, declared); e.Endpoint.BaseURL != declared.BaseURL || e.Endpoint.Token != "tok" {
		t.Fatalf("declared endpoint with a token override: got %+v", e)
	}
	t.Setenv("BNBLOC_CORE_ACME_URL", "http://127.0.0.1:7001")
	if e := EntryFor("acme", p, declared); !e.Explicit || e.Endpoint.BaseURL != "http://127.0.0.1:7001" || e.Endpoint.Token != "tok" {
		t.Fatalf("URL override must win over the declared endpoint: got %+v", e)
	}
}

// Core entries have one builder. Outside this package no production code
// discovers a core's endpoint for an entry or reads the per-core env, so the
// gateway and the bridge cannot drift on what BNBLOC_CORE_<NAME>_URL means
// (2026-10-12: the gateway built installed cores a second way and ignored
// it).
func TestCoreEntriesBuiltOnlyByEntryFor(t *testing.T) {
	allowed := map[string]string{
		// The dev discovery probe reports the discovered address itself; it
		// builds no entry.
		"services/gateway/localagent.go": "dev discovery probe",
		// Conformance checks a provider's Discover contract.
		"core/conformance/conformance.go": "conformance check",
	}
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "internal/coreset/") {
			return
		}
		if _, ok := allowed[rel]; ok {
			return
		}
		for _, forbidden := range []string{"(core.Discoverer)", `"BNBLOC_CORE_`} {
			if strings.Contains(string(src), forbidden) {
				t.Errorf("%s uses %s: build core entries with coreset.EntryFor / FromEnv", rel, forbidden)
			}
		}
	})
}
