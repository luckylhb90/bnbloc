package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// seatProv is a restartProv that mints a distinct workspace per create.
type seatProv struct {
	restartProv
	workspaces int
}

func (p *seatProv) CreateWorkspace(context.Context, corepkg.Endpoint) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workspaces++
	return "w" + strconv.Itoa(p.workspaces), nil
}

// A member borrowing an agent takes a seat of their own
// (docs/prd/2026-10-14-multi-member-rooms.md rule 1): the owner's turns and
// the borrower's never share a session or a workspace — so the owner's
// conversation, attachments and Library listing never reach the borrower's
// turn — and a bridge restart keeps them apart.
func TestBorrowerTakesASeatOfTheirOwn(t *testing.T) {
	dir := t.TempDir()
	gw := newSandboxGateway(t, "room-1", false)
	prov := &seatProv{}
	cfg := Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov), StateDir: dir}
	b := New(cfg)
	ctx := context.Background()

	b.runBridgeJob(ctx, job("job-1", "alice"))
	b.runBridgeJob(ctx, job("job-2", "bob"))
	if prov.workspaces != 2 || prov.created != 2 || len(prov.inputs) != 0 {
		t.Fatalf("workspaces %d, sessions %d, inputs %v; want the borrower in a workspace and session of their own", prov.workspaces, prov.created, prov.inputs)
	}
	// Each one's next turn continues its own session.
	b.runBridgeJob(ctx, job("job-3", "alice"))
	b.runBridgeJob(ctx, job("job-4", "bob"))
	if len(prov.inputs) != 2 || prov.inputs[0] != "s1" || prov.inputs[1] != "s2" {
		t.Fatalf("inputs %v; want alice's turn in s1 and bob's in s2", prov.inputs)
	}

	raw, err := os.ReadFile(filepath.Join(dir, workspacesFile))
	if err != nil || !strings.Contains(string(raw), `"requester": "bob"`) {
		t.Fatalf("persisted bindings = %s (%v); want bob's seat kept apart", raw, err)
	}
	// After a restart each seat resumes its own session.
	r := New(cfg)
	r.runBridgeJob(ctx, job("job-5", "bob"))
	r.runBridgeJob(ctx, job("job-6", "alice"))
	if prov.created != 2 || len(prov.inputs) != 4 || prov.inputs[2] != "s2" || prov.inputs[3] != "s1" {
		t.Fatalf("after a restart: sessions %d, inputs %v; want bob back in s2 and alice in s1", prov.created, prov.inputs)
	}
}

// A borrower's deliverable is not a version of the owner's file of the
// same name: the seat is part of its id.
func TestSeatIsPartOfADeliverablesID(t *testing.T) {
	own := &roomSession{owner: "alice", core: "core-a", provider: "codex", seat: driveKey{room: "room-1", core: "core-a", provider: "codex"}}
	bobs := &roomSession{owner: "alice", core: "core-a", provider: "codex", seat: driveKey{room: "room-1", core: "core-a", provider: "codex", requester: "bob"}}
	if artifactIDForFile("room-1", own, "report.md") == artifactIDForFile("room-1", bobs, "report.md") {
		t.Fatal("the borrower's report.md shares the owner's artifact id")
	}
	// The owner's own seat keeps the id it always had.
	legacy := &roomSession{owner: "alice", core: "core-a", provider: "codex"}
	if artifactIDForFile("room-1", own, "report.md") != artifactIDForFile("room-1", legacy, "report.md") {
		t.Fatal("the owner's deliverable id changed")
	}
}

// A bridge serving every owner keeps each owner's agent in its own slot.
func TestServeAllOwnersKeepsOwnersApart(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	prov := &seatProv{}
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", ServeAllOwners: true, Cores: newTestCore(prov)})
	ctx := context.Background()
	carol := job("job-2", "carol")
	carol.OwnerUserID = "carol"
	b.runBridgeJob(ctx, job("job-1", "alice"))
	b.runBridgeJob(ctx, carol)
	if prov.workspaces != 2 || prov.created != 2 || len(prov.inputs) != 0 {
		t.Fatalf("workspaces %d, sessions %d, inputs %v; want carol's agent apart from alice's", prov.workspaces, prov.created, prov.inputs)
	}
}
