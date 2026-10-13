package bridge

import (
	"context"
	"sync"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// refreshProv is a sandboxExec core that takes a fresh execution
// (executionRefresh), stays busy for a while, and records each refresh.
type refreshProv struct {
	sandboxProv
	refresh   bool
	busyUntil time.Time
	rmu       sync.Mutex
	refreshed []string // the token of each refresh
}

func (p *refreshProv) Supports(ctx context.Context, ep corepkg.Endpoint, c corepkg.Capability) bool {
	if c == corepkg.CapabilityExecutionRefresh {
		return p.refresh
	}
	return p.sandboxProv.Supports(ctx, ep, c)
}

func (p *refreshProv) RefreshExecution(_ context.Context, _ corepkg.Endpoint, _, _ string, exec *corepkg.Execution) error {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	p.refreshed = append(p.refreshed, exec.Sandbox.ExecToken)
	return nil
}

func (p *refreshProv) PollTurn(ctx context.Context, ep corepkg.Endpoint, ws, sid string) (corepkg.TurnStatus, error) {
	if time.Now().Before(p.busyUntil) {
		return corepkg.TurnStatus{}, nil
	}
	return p.sandboxProv.PollTurn(ctx, ep, ws, sid)
}

func (p *refreshProv) tokens() []string {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	return append([]string(nil), p.refreshed...)
}

// A sandboxed turn on a core that takes a fresh execution starts with a
// short-lived token and has it renewed while it runs, never after; on a
// core that cannot, its one token is minted to outlive long turns.
func TestSandbox_runningTurnRenewsItsExecToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refresh bool
		ttl     time.Duration
	}{
		{"renewed", true, sandboxExecTokenTTL},
		{"no refresh", false, sandboxExecTokenNoRefreshTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			sbx := newFakeSandboxd(t)
			prov := &refreshProv{sandboxProv: sandboxProv{supports: true}, refresh: tc.refresh}
			prov.targets = []corepkg.Target{{ID: "c", Provider: "codex"}}
			b, _ := sandboxBridge(t, gw, sbx, SandboxBorrowed, prov)
			b.execRenewInterval = 30 * time.Millisecond
			prov.busyUntil = time.Now().Add(1500 * time.Millisecond)

			b.runBridgeJob(context.Background(), job("job-1", "bob"))
			afterTurn := len(prov.tokens())
			time.Sleep(150 * time.Millisecond)

			sbx.mu.Lock()
			ttls := append([]int64(nil), sbx.ttls...)
			sbx.mu.Unlock()
			if len(ttls) == 0 || ttls[0] != int64(tc.ttl/time.Second) {
				t.Fatalf("first token TTL = %v; want %v", ttls, tc.ttl)
			}
			got := prov.tokens()
			if !tc.refresh {
				if len(got) != 0 || len(ttls) != 1 {
					t.Fatalf("refreshes %v, mints %v; want none renewed", got, ttls)
				}
				return
			}
			if len(got) < 2 || got[0] == got[1] || got[0] == "tok-1" {
				t.Fatalf("refreshed tokens = %v; want fresh ones while the turn ran", got)
			}
			for _, ttl := range ttls {
				if ttl != int64(sandboxExecTokenTTL/time.Second) {
					t.Fatalf("token TTLs = %v; every renewal short-lived", ttls)
				}
			}
			if len(got) != afterTurn {
				t.Fatalf("renewed after the turn ended: %d then %d", afterTurn, len(got))
			}
		})
	}
}
