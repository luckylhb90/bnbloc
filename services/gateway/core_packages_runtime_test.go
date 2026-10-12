package gateway

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
)

func manifestPkg(m manifestcore.Manifest) pkgmeta.Package {
	return pkgmeta.Package{Kind: pkgmeta.KindCorePackageV1, Name: m.Name, Version: "0.0.0", Runtime: pkgmeta.RuntimeManifest, Manifest: &m}
}

func validProbeManifest(t *testing.T, discover *manifestcore.DiscoverOp) manifestcore.Manifest {
	t.Helper()
	m := manifestcore.Manifest{
		Name:          "probe-test",
		CreateWksp:    manifestcore.Op{Method: "POST", Path: "/w"},
		CreateSession: manifestcore.Op{Method: "POST", Path: "/s"},
		SendInput:     manifestcore.Op{Method: "POST", Path: "/i"},
		ListTargets:   manifestcore.Op{Method: "GET", Path: "/t"},
		Messages:      manifestcore.Op{Method: "GET", Path: "/m", Items: &manifestcore.ItemsSpec{List: "messages", Fields: map[string]string{"text": "text"}}},
		PollTurn:      manifestcore.Op{Method: "GET", Path: "/turn"},
		Discover:      discover,
	}
	if _, err := manifestcore.New(m); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	return m
}

// A listener-discovery manifest describes a daemon on the user's machine: with
// no listener file present the probe has nothing to reach, and the install
// must still succeed (schema validation is that form's gate).
func TestProbeAllowsListenerOnlyManifestWithoutDaemon(t *testing.T) {
	m := validProbeManifest(t, &manifestcore.DiscoverOp{Listener: &manifestcore.ListenerSpec{
		PathEnv: "PROBE_TEST_LISTENER", AddrField: "addr", TokenField: "auth.token",
	}})
	t.Setenv("PROBE_TEST_LISTENER", "/nonexistent/probe-test.listener.json")

	svc := &Service{}
	err := svc.probeCorePackage(context.Background(), manifestPkg(m), extgov.Guard{})
	if err != nil {
		t.Fatalf("listener-only manifest install must not require a reachable daemon: %v", err)
	}
}

// A probe-based manifest that resolves to nothing is a broken install: the
// probe must keep failing closed for that form.
func TestProbeStillFailsForUnreachableProbeURL(t *testing.T) {
	m := validProbeManifest(t, &manifestcore.DiscoverOp{ProbeURL: "http://127.0.0.1:1", ProbePath: "/v1/info"})

	svc := &Service{}
	err := svc.probeCorePackage(context.Background(), manifestPkg(m), extgov.Guard{})
	if err == nil {
		t.Fatal("unreachable probe URL must fail the install")
	}
}
