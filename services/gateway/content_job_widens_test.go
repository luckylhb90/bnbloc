package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A job's grant never widens who sees content (errJobWidens): a new
// version an agent saves for maya of a component her organization uses
// stays hers, whatever the organization's approval setting (no request,
// nothing published), and the save says the version is proposed. Her own
// save still goes to the organization as before, and her click publishes
// the proposed version.
func TestJobSaveNeverReachesTheOrganization(t *testing.T) {
	g := newSharingGateway(t)
	ctx := tenant.NewContext(context.Background(), "home")
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "opencode",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	src := g.create("maya", expertForm("Review carefully."))
	g.setApproval(wire.PublishApprovalNone)
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org"}); code != http.StatusCreated {
		t.Fatalf("publish = %d %s", code, raw)
	}
	pending := func() int {
		t.Helper()
		reqs, err := g.svc.ContentComponents.ListContentRequests(context.Background(), store.ContentRequestQuery{ComponentID: src.ID, Status: store.ContentRequestPending})
		if err != nil {
			t.Fatal(err)
		}
		return len(reqs)
	}
	for i, setting := range []string{wire.PublishApprovalNone, wire.PublishApprovalRequired} {
		g.setApproval(setting)
		form := expertForm("Review with care, take " + setting + ".")
		form["baseVersion"] = src.LatestVersion
		code, raw := g.json("jack", http.MethodPost, "/v1/content-components/"+src.ID+"/versions?job=job-1&room=room-1", form)
		d := decodeDetail(t, raw)
		want := []string{"1.0.1", "1.0.2"}[i]
		if code != http.StatusCreated || d.OrgProposed != want || d.OrgPublished != "" || d.Resubmitted != nil {
			t.Fatalf("%s: a job's new version = %d %+v", setting, code, d)
		}
		src = d.Component
		if org := findContent(g.library("jack", "expert"), "reviewer", "org"); org == nil || org.LatestVersion != "1.0.0" {
			t.Fatalf("%s: the organization's = %+v (a job's save reached it)", setting, org)
		}
		if n := pending(); n != 0 {
			t.Fatalf("%s: %d requests opened by a job's save", setting, n)
		}
	}
	// Her click publishes the proposed version (the publish card).
	g.setApproval(wire.PublishApprovalNone)
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org", Version: "1.0.2"}); code != http.StatusCreated {
		t.Fatalf("her click = %d %s", code, raw)
	}
	if org := findContent(g.library("jack", "expert"), "reviewer", "org"); org == nil || org.LatestVersion != "1.0.2" {
		t.Fatalf("after her click = %+v", org)
	}
	// Her own save still goes to the organization by itself.
	form := expertForm("Her own words.")
	form["baseVersion"] = "1.0.2"
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/versions", form)
	if d := decodeDetail(t, raw); code != http.StatusCreated || d.OrgPublished != "1.0.3" || d.OrgProposed != "" {
		t.Fatalf("her own save = %d %+v", code, d)
	}
}
