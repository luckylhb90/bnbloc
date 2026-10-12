package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// H3: a core installed through replica A appears in replica B's core list at
// B's next reconcile, and leaves it when A uninstalls it.
func TestComponentReconcileReachesOtherReplica(t *testing.T) {
	ctx := context.Background()
	settings := store.NewMemoryPlatformSettings()
	a := &Service{PlatformSettings: settings, AllowExtUnsigned: true}
	b := &Service{PlatformSettings: settings, AllowExtUnsigned: true}

	manifest, err := manifestcore.ParseManifest([]byte(`{
      "name": "replicated",
      "createWorkspace": {"method":"POST","path":"/w"},
      "createSession": {"method":"POST","path":"/s"},
      "sendInput": {"method":"POST","path":"/i"},
      "listTargets": {"method":"GET","path":"/t"},
      "messages": {"method":"GET","path":"/m","items":{"list":"messages","fields":{"text":"text"}}},
      "pollTurn": {"method":"GET","path":"/turn"}
    }`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(legacyManifestPackage(manifest))
	t.Cleanup(func() { _ = core.Unregister("replicated") })
	if _, err := a.legacyManifestInstallPipeline().Install(ctx, extpipe.InstallRequest{Body: body, Actor: "admin"}); err != nil {
		t.Fatalf("install on A: %v", err)
	}
	if b.coreByName("replicated") != nil {
		t.Fatal("B has the core before reconciling")
	}
	b.reconcileComponents(ctx)
	if b.coreByName("replicated") == nil {
		t.Fatal("B did not pick up A's install")
	}
	if err := a.corePipeline().Uninstall(ctx, core.KindCore, "replicated", "admin"); err != nil {
		t.Fatalf("uninstall on A: %v", err)
	}
	b.reconcileComponents(ctx)
	if b.coreByName("replicated") != nil {
		t.Fatal("B kept a core A uninstalled")
	}
}
