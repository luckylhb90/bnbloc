package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Handoff package (F4): hand a room's work to someone else, or continue it in
// another deployment. The room service orchestrates both directions because it
// owns the room, its members, channels and artifacts; realtime owns the feed
// (and its per-room sequence) and the attachment blobs, reached through the
// HandoffFeed port. The package format is docs/room-export-format.md; its DTOs
// live in internal/wire/handoff.go.
//
//   GET  /v1/rooms/{roomId}/export   owner/admin: stream the package (zip)
//   POST /v1/rooms/import            any signed-in member: create a NEW room
//                                    from a package (multipart field "file")
//   GET  /v1/rooms/{roomId}/handoff  member: the room's import provenance
//
// Export is gated to owner/admin — the roles the room model already reserves
// for acting on the room as a whole (invites, member management, channel
// deletion): a package is a bulk copy of every member's messages, files and
// artifacts that leaves the room's access control entirely, so it is a
// room-level action, not a per-message read.

// Package bounds. They are product safety limits of the package protocol
// (like the chat text cap), not deployment data:
//   - maxHandoffPackageBytes caps the uploaded archive; it matches realtime's
//     generic blob upload cap, the largest single object the platform stores.
//   - maxHandoffExpandedBytes caps the sum of the archive's DECLARED
//     uncompressed entry sizes (archive/zip refuses entries that exceed their
//     declared size), so a small archive cannot expand into unbounded work.
//   - per-file limits reuse the existing caps: artifact content uses
//     maxArtifactContentBytes, attachments go through realtime's upload route
//     (its type gate and size cap), a messages.jsonl line is one feed row.
const (
	maxHandoffPackageBytes  = 256 << 20
	maxHandoffExpandedBytes = 1 << 30
	maxHandoffManifestBytes = 1 << 20
	maxHandoffMetaBytes     = 64 << 10
	// maxHandoffLineBytes is one messages.jsonl line: realtime's 1 MiB event
	// payload cap plus the row framing.
	maxHandoffLineBytes        = 1<<20 + 64<<10
	maxHandoffMessages         = 100_000
	maxHandoffChannels         = 200
	maxHandoffMembers          = 5_000
	maxHandoffArtifacts        = 1_000
	maxHandoffArtifactVersions = 1_000
	// handoffFeedPage is the feed page size the export reads with.
	handoffFeedPage = 500
)

// HandoffFeed is the room service's port onto realtime's side of a handoff:
// the persisted feed, the attachment blobs, and the history-only import write.
// Every call acts as user (the end user), so realtime enforces that user's
// membership. internal/realtime.HandoffClient implements it over HTTP.
type HandoffFeed interface {
	Feed(ctx context.Context, user, roomID string, after int64, limit int) ([]wire.FeedMessage, error)
	OpenAttachment(ctx context.Context, user, roomID, attachmentID string) (io.ReadCloser, error)
	UploadAttachment(ctx context.Context, user, roomID, filename, contentType string, content io.Reader) (string, error)
	ImportMessages(ctx context.Context, user, roomID string, batch wire.RoomImportBatch) (wire.RoomImportBatchReply, error)
}

// handleGetHandoff returns a room's import provenance to members: the source
// room, when/by whom it was exported and imported, and the source member list
// (invite suggestions only). 404 for a room that was not imported.
func (s *Service) handleGetHandoff(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.Store.IsMember(r.Context(), roomID, caller(r)) {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	if s.RoomImports == nil {
		httpx.Error(w, "room was not imported", http.StatusNotFound)
		return
	}
	info, ok, err := s.RoomImports.GetRoomImport(r.Context(), roomID)
	if err != nil {
		obs.ServerError(w, "handoff lookup failed", err)
		httpx.Error(w, "handoff lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, "room was not imported", http.StatusNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, info)
}

// feedPayload is the read view of a feed row's payload the handoff needs: a
// chat row's attribution, attachments and recipient, or an agent batch's
// session and messages (one row is one kind; the other half stays zero).
type feedPayload struct {
	wire.ChatMessagePayload
	AgentSessionID string              `json:"agentSessionId"`
	Messages       []wire.AgentMessage `json:"messages"`
}

// attachmentRef is the attachment metadata a chat payload embeds.
type attachmentRef = wire.ChatAttachment

func decodeFeedPayload(raw json.RawMessage) feedPayload {
	p, _ := wire.DecodePayload[feedPayload](raw)
	return p
}

// agentHandle renders an agent's room handle (owner/provider).
func agentHandle(owner, provider string) string {
	switch {
	case owner != "" && provider != "":
		return owner + "/" + provider
	case provider != "":
		return provider
	case owner != "":
		return owner + "/agent"
	default:
		return "agent"
	}
}

// packageSegment makes an id safe as one archive path segment.
func packageSegment(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "_"
	}
	return out
}

// agentFeedRow reports whether a feed row involves an agent: an agent reply
// batch, or a chat message addressed to an agent.
func agentFeedRow(m wire.FeedMessage) bool {
	if m.Kind == wire.KindAgentMessages {
		return true
	}
	p := decodeFeedPayload(m.Payload)
	return p.Recipient != nil && p.Recipient.Provider != ""
}
