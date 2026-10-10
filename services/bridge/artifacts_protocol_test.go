package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// A Core Protocol daemon advertising sessionFiles + sessionFileBytes is a
// deliverable source through the real protocol client: the bridge lists the
// session files over the wire and publishes a binary deliverable with the
// exact bytes the daemon served.
func TestPublishDeliverablesFromProtocolCore(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("\x00", 32)
	md := "# Report\n"
	now := time.Now().UnixMilli()
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case protocol.PathDiscover:
			_ = json.NewEncoder(w).Encode(protocol.Discovery{Name: "proto", Version: "1", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"},
				Capabilities: []string{protocol.CapabilitySessionFiles, protocol.CapabilitySessionFileBytes}})
		case protocol.SessionFilesPath("ws", "s"):
			_ = json.NewEncoder(w).Encode(protocol.ListSessionFilesResponse{Files: []protocol.SessionFile{
				{Path: "assets/chart.png", SizeBytes: int64(len(png)), ModTimeMs: now},
				{Path: "report.md", SizeBytes: int64(len(md)), ModTimeMs: now},
			}})
		case protocol.WorkspaceFilePath("ws", "assets/chart.png"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte(png))
		case protocol.WorkspaceFilePath("ws", "report.md"):
			_, _ = w.Write([]byte(md))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(daemon.Close)
	cg := newCaptureGateway(t)
	b := New(Config{GatewayBaseURL: cg.srv.URL, GatewayToken: "t", OwnerSubject: "maya", Cores: []CoreConfig{{
		Name: "proto", Provider: protocol.NewClient("proto", daemon.Client()), Endpoint: corepkg.Endpoint{BaseURL: daemon.URL},
	}}})
	rs := &roomSession{core: "proto", workspaceID: "ws", sessionID: "s"}

	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: now, JobID: "job-1", SourceSeq: 7})

	kinds := map[string]any{}
	for _, c := range cg.creates {
		kinds[c["title"].(string)] = c["kind"]
	}
	if kinds["assets/chart.png"] != store.ArtifactKindImage || kinds["report.md"] != store.ArtifactKindMarkdown {
		t.Fatalf("creates = %v, want the png (image) and the markdown", cg.creates)
	}
	var sawPNG, sawMD bool
	for _, p := range cg.publishes {
		switch {
		case p["fileName"] == "chart.png":
			sawPNG = p["file"] == png && p["contentType"] == "image/png"
		case p["content"] == md:
			sawMD = true
		}
	}
	if !sawPNG || !sawMD {
		t.Fatalf("publishes = %v, want the exact png bytes as a blob and the markdown inline", cg.publishes)
	}
}
