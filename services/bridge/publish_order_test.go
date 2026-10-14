package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

// publishTestDaemon is a Core Protocol daemon whose session holds one
// markdown file; list delays its file listing.
func publishTestDaemon(t *testing.T, list time.Duration) *httptest.Server {
	md := "# Report\n"
	now := time.Now().UnixMilli()
	d := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case protocol.PathDiscover:
			_ = json.NewEncoder(w).Encode(protocol.Discovery{Name: "proto", Version: "1", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"},
				Capabilities: []string{protocol.CapabilitySessionFiles, protocol.CapabilitySessionFileBytes}})
		case protocol.SessionFilesPath("ws", "s"):
			time.Sleep(list)
			_ = json.NewEncoder(w).Encode(protocol.ListSessionFilesResponse{Files: []protocol.SessionFile{{Path: "report.md", SizeBytes: int64(len(md)), ModTimeMs: now}}})
		case protocol.WorkspaceFilePath("ws", "report.md"):
			_, _ = w.Write([]byte(md))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(d.Close)
	return d
}

func publishTestDrive(b *Bridge) *drive {
	d := &drive{job: bridgeJob{JobID: "job-1", RoomID: "room-1", ChannelID: "ch-1", MessageSeq: 7}, run: &runningJob{}, startMs: time.Now().Add(-time.Minute).UnixMilli()}
	d.run.bind("proto", &roomSession{core: "proto", workspaceID: "ws", sessionID: "s"})
	return d
}

// A turn's deliverables are published before its outcome is reported
// (docs/prd/2026-10-14-multi-member-rooms.md rule 7): publishTurn returns
// once they are.
func TestDeliverablesArePublishedBeforeTheOutcome(t *testing.T) {
	daemon := publishTestDaemon(t, 0)
	cg := newCaptureGateway(t)
	b := New(Config{GatewayBaseURL: cg.srv.URL, GatewayToken: "t", OwnerSubject: "maya", Cores: []CoreConfig{{
		Name: "proto", Provider: protocol.NewClient("proto", daemon.Client()), Endpoint: corepkg.Endpoint{BaseURL: daemon.URL},
	}}})
	b.publishTurn(context.Background(), publishTestDrive(b))
	cg.mu.Lock()
	defer cg.mu.Unlock()
	if len(cg.creates) != 1 {
		t.Fatalf("creates = %v when publishTurn returned; want report.md already published", cg.creates)
	}
}

// A slow publish never holds the outcome past publishWait: it finishes
// after the outcome is reported.
func TestASlowPublishDoesNotHoldTheOutcome(t *testing.T) {
	daemon := publishTestDaemon(t, 2*time.Second)
	cg := newCaptureGateway(t)
	b := New(Config{GatewayBaseURL: cg.srv.URL, GatewayToken: "t", OwnerSubject: "maya", Cores: []CoreConfig{{
		Name: "proto", Provider: protocol.NewClient("proto", daemon.Client()), Endpoint: corepkg.Endpoint{BaseURL: daemon.URL},
	}}})
	b.publishWaitOverride = 100 * time.Millisecond
	start := time.Now()
	b.publishTurn(context.Background(), publishTestDrive(b))
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("publishTurn waited %s; want at most the publish wait", waited)
	}
}
