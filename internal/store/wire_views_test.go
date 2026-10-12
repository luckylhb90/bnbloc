package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// A job's member view keeps the server-side fields out (decision O4) and
// names what a member reads from the bridge's diagnostics.
func TestAgentJobViewProjectsDiagnostics(t *testing.T) {
	j := AgentJob{
		JobID: "job-1", TenantID: "org-a", RoomID: "room-1", Status: AgentJobFailed, FailureReason: FailureRunnerFailed,
		LeaseBridgeID: "bridge-maya", LeaseExpiresAtMs: 9,
		DiagnosticsJSON: json.RawMessage(`{"error":"the core exited","sessionId":"sess-1","contentMissing":["lint@^1"],"workspaceId":"/home/maya/w"}`),
	}
	v := j.View()
	if v.Error != "the core exited" || v.ReplySessionID != "sess-1" || len(v.ContentMissing) != 1 || v.Status != "failed" || v.FailureReason != "runner_failed" {
		t.Fatalf("view = %+v", v)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"org-a", "bridge-maya", "/home/maya/w", "leaseExpiresAtMs", "diagnostics"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the member view carries %q: %s", leak, raw)
		}
	}
	// A malformed report projects nothing rather than failing the event.
	j.DiagnosticsJSON = json.RawMessage(`{"error":`)
	if v := j.View(); v.Error != "" || v.JobID != "job-1" {
		t.Fatalf("malformed diagnostics view = %+v", v)
	}
}

// An artifact version's member view carries no blob key or tenant.
func TestArtifactVersionViewHidesStorage(t *testing.T) {
	v := ArtifactVersion{ArtifactID: "a", TenantID: "org-a", Version: 2, Storage: ArtifactStorageBlob, BlobKey: "sha256/abc", ContentHash: "abc", ThumbnailBlobKey: "sha256/def", Thumbnail: &ArtifactThumbnail{MimeType: "image/png", Width: 1, Height: 1}}
	raw, err := json.Marshal(v.View())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"org-a", "sha256/abc", "sha256/def"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the member view carries %q: %s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), `"thumbnail":{"mimeType":"image/png"`) {
		t.Errorf("thumbnail dropped: %s", raw)
	}
}
