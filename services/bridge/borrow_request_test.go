package bridge

import (
	"context"
	"strings"
	"testing"
)

// A borrow refused because the owner authorized none tells the borrower
// the owner was asked to approve it, when the authority recorded that
// request (docs/prd/2026-10-14-multi-member-rooms.md rule 9).
func TestRefusedBorrowSaysAccessWasRequested(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	gw.deny, gw.accessRequested = "owner_not_authorized", true
	prov := &seatProv{}
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	b.runBridgeJob(context.Background(), job("job-1", "bob"))
	_, rows := gw.stored()
	found := false
	for _, m := range rows {
		p, _ := m.Payload.(map[string]any)
		if text := payloadText(m); strings.Contains(text, "has not authorized borrowing") && strings.Contains(text, "asked to approve") && p["accessRequested"] == true {
			found = true
		}
	}
	if !found || prov.created != 0 {
		t.Fatalf("rows %q, sessions %d; want the refusal saying access was requested, nothing driven", storedTexts(rows), prov.created)
	}
}
