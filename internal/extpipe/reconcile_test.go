package extpipe

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// replicaPair returns two pipelines over one settings store: replica A takes
// the admin's lifecycle calls, replica B only reconciles.
func replicaPair(t *testing.T) (a, b *Pipeline, bsw *switchable) {
	t.Helper()
	a, _ = newSwitchablePipeline(t)
	b, bsw = newSwitchablePipeline(t)
	b.Settings, b.Audit = a.Settings, a.Audit
	return a, b, bsw
}

// H3: an install, upgrade, switch-off and uninstall made on replica A reach
// replica B through B's periodic reconcile; an unchanged reconcile is a no-op.
func TestReconcilePropagatesLifecycleAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	a, b, bsw := replicaPair(t)
	t.Cleanup(func() { _ = extreg.Unregister(switchableKind, "repl") })

	if _, err := a.Install(ctx, InstallRequest{Body: switchablePkg("repl", "1.0.0"), Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	rep, err := b.Reconcile(ctx, extreg.ServiceGateway)
	if err != nil || len(rep.Activated) != 1 || len(bsw.activations) != 1 {
		t.Fatalf("install reconcile = %+v %v (activations %v)", rep, err, bsw.activations)
	}
	if rep, _ := b.Reconcile(ctx, extreg.ServiceGateway); rep.Changed() || len(bsw.activations) != 1 {
		t.Fatalf("idle reconcile changed %+v (activations %v)", rep, bsw.activations)
	}

	if _, err := a.Install(ctx, InstallRequest{Body: switchablePkg("repl", "2.0.0"), Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	if rep, _ := b.Reconcile(ctx, extreg.ServiceGateway); len(rep.Activated) != 1 || bsw.activations[len(bsw.activations)-1] != "2.0.0" {
		t.Fatalf("upgrade reconcile = %+v (activations %v)", rep, bsw.activations)
	}

	if err := a.SetRollout(ctx, switchableKind, "repl", RolloutOff, "admin"); err != nil {
		t.Fatal(err)
	}
	if rep, _ := b.Reconcile(ctx, extreg.ServiceGateway); len(rep.Deactivated) != 1 || len(bsw.deactivated) != 1 {
		t.Fatalf("rollout-off reconcile = %+v (deactivated %v)", rep, bsw.deactivated)
	}

	if err := a.SetRollout(ctx, switchableKind, "repl", RolloutAuto, "admin"); err != nil {
		t.Fatal(err)
	}
	if rep, _ := b.Reconcile(ctx, extreg.ServiceGateway); len(rep.Activated) != 1 {
		t.Fatalf("re-enable reconcile = %+v", rep)
	}
	if err := a.Uninstall(ctx, switchableKind, "repl", "admin"); err != nil {
		t.Fatal(err)
	}
	if rep, _ := b.Reconcile(ctx, extreg.ServiceGateway); len(rep.Deactivated) != 1 {
		t.Fatalf("uninstall reconcile = %+v", rep)
	}
}

// A record whose activation fails is audited once, not retried every tick,
// and retried as soon as its content changes.
func TestReconcileDoesNotRetryFailedRecordUntilItChanges(t *testing.T) {
	ctx := context.Background()
	a, b, bsw := replicaPair(t)
	t.Cleanup(func() { _ = extreg.Unregister(switchableKind, "broken") })
	if _, err := a.Install(ctx, InstallRequest{Body: switchablePkg("broken", "1.0.0"), Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	bsw.failActivate["1.0.0"] = true
	if rep, _ := b.Reconcile(ctx, extreg.ServiceGateway); len(rep.Failed) != 1 {
		t.Fatalf("first reconcile = %+v; want one failure", rep)
	}
	if rep, _ := b.Reconcile(ctx, extreg.ServiceGateway); rep.Changed() {
		t.Fatalf("second reconcile retried the broken record: %+v", rep)
	}
	if _, err := a.Install(ctx, InstallRequest{Body: switchablePkg("broken", "1.0.1"), Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	if rep, _ := b.Reconcile(ctx, extreg.ServiceGateway); len(rep.Activated) != 1 {
		t.Fatalf("fixed record not activated: %+v", rep)
	}
}
