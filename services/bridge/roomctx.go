package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// Pull-based room context wiring.
//
// When the core can receive workspace files (core.WorkspaceFileWriter, probed
// through core.As so a manifest/protocol core reports it per its ops/discovery), the
// bridge materializes the room's shared history into .room/ inside the agent's
// workspace — chunks + index + recent + deliverables — and drives with a
// pointer prompt. The instruction is never diluted by history; the agent
// consults .room/ with its own file tools when the task needs context. Cores
// without the capability keep the push-based prompt (driveText), so behavior
// degrades per-core rather than being removed.

// writtenAttachment describes an attachment materialized into the workspace: the
// path the agent can open, plus its original name and type for the prompt.
type writtenAttachment struct {
	Path        string
	Filename    string
	ContentType string
}

// materializeAttachments downloads each chat attachment and writes it under
// .room/attachments/<id><ext> so a binary-capable core can open it with its own
// file tools, and returns descriptors so the turn's prompt can name the files.
// It uses the WorkspaceFileBytesWriter seam; a core without that capability
// returns nil (nothing to open). Best-effort per attachment: a failed
// fetch/write logs and is skipped rather than failing the turn.
func (b *Bridge) materializeAttachments(ctx context.Context, w corepkg.WorkspaceFileBytesWriter, ep corepkg.Endpoint, room, workspaceID string, ids []string) []writtenAttachment {
	if len(ids) == 0 || w == nil {
		return nil
	}
	var out []writtenAttachment
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		blob, contentType, filename, err := b.gatewayGETAttachment(ctx, "/v1/rooms/"+room+"/attachments/"+id)
		if err != nil {
			slog.Debug("bnbloc bridge attachment fetch failed", "event", "bnbloc.bridge.attachment_fetch_failed", "room", room, "attachment", id, "error", err)
			continue
		}
		path := ".room/attachments/" + id + extForContentType(contentType, filename)
		if err := w.WriteWorkspaceFileBytes(ctx, ep, workspaceID, path, blob); err != nil {
			slog.Debug("bnbloc bridge attachment write failed", "event", "bnbloc.bridge.attachment_write_failed", "room", room, "attachment", id, "error", err)
			continue
		}
		out = append(out, writtenAttachment{Path: path, Filename: filename, ContentType: contentType})
	}
	return out
}

// extForContentType maps an attachment's content-type (validated against the
// upload allow-list, so authoritative) to a file extension, falling back to the
// original filename's extension, else "". The extension lets the agent's tools
// recognize the file (an image tool needs .png/.jpg, not an opaque blob).
func extForContentType(contentType, filename string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	}
	if dot := strings.LastIndexByte(filename, '.'); dot >= 0 && dot+1 < len(filename) {
		return filename[dot:]
	}
	return ""
}

// roomArtifactsMd renders the room's current deliverables as a compact record:
// title, kind, version, last author. This is the room's live state (what
// exists now), deliberately distinct from the conversation history.
func (b *Bridge) roomArtifactsMd(ctx context.Context, room string) roomlog.NamedFile {
	raw, err := b.gatewayGET(ctx, "/v1/rooms/"+room+"/artifacts")
	if err != nil {
		return roomlog.NamedFile{Name: ".room/artifacts.md", Content: "# Room deliverables\n\n(none)\n"}
	}
	var parsed struct {
		Artifacts []struct {
			Title          string `json:"title"`
			Kind           string `json:"kind"`
			CurrentVersion int    `json:"currentVersion"`
			CreatedBy      string `json:"createdBy"`
		} `json:"artifacts"`
	}
	_ = json.Unmarshal(raw, &parsed)
	var sb strings.Builder
	sb.WriteString("# Room deliverables — current state (RECORDS, not requests)\n\n")
	if len(parsed.Artifacts) == 0 {
		sb.WriteString("(none yet)\n")
	}
	for _, a := range parsed.Artifacts {
		if a.Title == "" {
			continue
		}
		author := a.CreatedBy
		if author == "" {
			author = "?"
		}
		sb.WriteString("- " + a.Title + " (" + a.Kind + ") v" + strconv.Itoa(a.CurrentVersion) + " — by " + author + "\n")
	}
	return roomlog.NamedFile{Name: ".room/artifacts.md", Content: sb.String()}
}

// roomLiveMd renders the recent live-artifact interaction trail: what members
// did inside running deliverables since the bridge started. This is the agent's
// eyes on interaction — a member's "the board looks wrong now" is answerable
// from the actual gestures. Artifacts are referenced by title-in-artifacts.md
// terms (id prefix) so the agent can cross-reference the deliverable.
func (b *Bridge) roomLiveMd(room string) roomlog.NamedFile {
	trail := b.liveTrail(room)
	var sb strings.Builder
	sb.WriteString("# Live interaction trail (what members did in running artifacts)\n\n")
	if len(trail) == 0 {
		sb.WriteString("(no live interactions yet)\n")
	} else {
		sb.WriteString("Newest last. `state` is the artifact's own live state at that moment.\n\n")
		for _, g := range trail {
			when := time.UnixMilli(g.AtUnixMs).UTC().Format("15:04:05")
			id := g.Artifact
			if len(id) > 12 {
				id = id[:12]
			}
			actor := g.Actor
			if actor == "" {
				actor = "?"
			}
			sb.WriteString("- " + when + " UTC · artifact " + id + " · by " + actor + " · state " + g.State + "\n")
		}
	}
	return roomlog.NamedFile{Name: ".room/live.md", Content: sb.String()}
}

// materializeRoomContext refreshes the .room/ tree in the given workspace and
// returns the updated state. dropText is the current turn's instruction: it is
// delivered as the [now] section of the prompt, so it is removed from the
// records (the newest matching entry only — an older identical message is
// genuine history). selfSpeaker is this agent's own "owner/provider" label; its
// replies from THIS session (Seq past the session-creation boundary, which a
// fresh pass records) are dropped — the CLI's memory already holds them. Its
// replies from before the boundary stay: a rebind (core or bridge restart)
// starts a blank session whose memory does NOT hold them, and they are the
// continuity the new session needs. Other speakers (people, other agents)
// always materialize: seeing each other is the point of a shared room. Files
// are written chunks → recent → artifacts → INDEX (INDEX last, so a partial
// write never advertises files that do not exist). State is only advanced on
// full success, so a failed pass retries cleanly next turn.
func (b *Bridge) materializeRoomContext(ctx context.Context, w corepkg.WorkspaceFileWriter, ep corepkg.Endpoint, room, workspaceID, dropText, selfSpeaker string, fresh bool, st roomlog.CtxState) (roomlog.CtxState, error) {
	entries, complete, err := roomlog.History(ctx, b.gatewayGET, room, roomlog.HistoryQuery{After: st.EndSeq})
	if err != nil {
		return st, err
	}
	entries = dropInstruction(entries, dropText)
	bound := st.SelfAfterSeq
	if fresh {
		// A brand-new session: everything fetched predates it, so all of
		// its own replies are prior sessions' and must stay.
		bound = noSelfBound
	}
	entries = dropSelf(entries, selfSpeaker, bound)
	files, newState := roomlog.Materialize(entries, complete, st)
	if fresh {
		// Record the session-creation boundary: everything materialized above
		// predates this session; its own replies from here on are the
		// session-memory redundancy the bound filters.
		newState.SelfAfterSeq = newState.EndSeq
	}
	// Insert artifacts.md and live.md before the trailing INDEX.
	art := b.roomArtifactsMd(ctx, room)
	liv := b.roomLiveMd(room)
	if n := len(files); n > 0 && files[n-1].Name == ".room/INDEX.md" {
		files = append(files[:n-1:n-1], art, liv, files[n-1])
	} else {
		files = append(files, art, liv)
	}
	for _, f := range files {
		if err := w.WriteWorkspaceFile(ctx, ep, workspaceID, f.Name, f.Content); err != nil {
			return st, err
		}
	}
	return newState, nil
}

// drivePrompt builds a turn's prompt. A pull-capable core gets the pointer
// prompt over materialized .room/ files (fresh sessions seed the tree before
// the session is created — the workspace exists, the session does not yet);
// any materialization failure falls back to the push model for this turn. A
// core without the writer capability uses the push model every turn.
func (b *Bridge) drivePrompt(ctx context.Context, d *drive, workspaceID string, atts []writtenAttachment, st *roomlog.CtxState, fresh bool) string {
	room, owner, provider, text := d.job.RoomID, d.owner, d.job.Provider, d.text
	self := agentSpeakerLabel(owner, provider)
	pa := toPromptAttachments(atts)
	entry, ok := b.coreEntry(d.core)
	if d.caps.writer == nil || !ok {
		return b.driveTextDelta(ctx, room, owner, provider, text, pa, st, fresh)
	}
	w, ep := d.caps.writer, entry.endpoint
	// Materialize: fresh sessions start from an empty state.
	start := *st
	if fresh {
		start = roomlog.CtxState{}
	}
	newSt, err := b.materializeRoomContext(ctx, w, ep, room, workspaceID, text, self, fresh, start)
	if err != nil {
		slog.Warn("bnbloc bridge room-context materialization failed; falling back to pushed context",
			"event", "bnbloc.bridge.roomctx_failed", "room", room, "core", d.core, "error", err)
		return b.driveTextDelta(ctx, room, owner, provider, text, pa, st, fresh)
	}
	*st = newSt
	return roomlog.ComposePointer(self, text, pa)
}

// toPromptAttachments maps the bridge's written descriptors to roomlog's prompt
// shape (the roomlog package owns the prompt wording and cannot import bridge).
func toPromptAttachments(atts []writtenAttachment) []roomlog.Attachment {
	if len(atts) == 0 {
		return nil
	}
	out := make([]roomlog.Attachment, 0, len(atts))
	for _, a := range atts {
		out = append(out, roomlog.Attachment{Path: a.Path, Filename: a.Filename, ContentType: a.ContentType})
	}
	return out
}

// agentSpeakerLabel mirrors roomlog's agent speaker label: "owner/provider",
// or just the provider when the owner is unknown.
func agentSpeakerLabel(owner, provider string) string {
	return roomlog.AgentSpeaker(owner, provider)
}

// driveTextDelta composes the push-model prompt: the room's shared context
// delivered inline instead of .room/ files. It carries the pull model's exact
// semantics — every channel of the room (not just the default), never the
// agent's own past replies (its session memory already holds them), never the
// current instruction twice (it is the [now] section) — and it is scoped by
// what the session has already seen:
//
//   - fresh session: a full window (older entries condensed by the Summarizer)
//   - follow-up turn: only the delta since the session's last push, and a
//     follow-up with nothing new sends the bare instruction
//
// The delta boundary lives in st.PushSeenSeq: every fetched row counts as seen
// the moment it is fetched, including rows then filtered out — the agent
// authored the self replies, and the instruction is delivered as [now].
func (b *Bridge) driveTextDelta(ctx context.Context, room, owner, provider, text string, atts []roomlog.Attachment, st *roomlog.CtxState, fresh bool) string {
	if st == nil {
		return text
	}
	self := agentSpeakerLabel(owner, provider)
	entries, _, err := roomlog.History(ctx, b.gatewayGET, room, roomlog.HistoryQuery{After: st.PushSeenSeq, MaxRows: b.recentRows})
	if err != nil {
		slog.Debug("bnbloc bridge room-context unavailable, sending bare text",
			"event", "bnbloc.bridge.context_unavailable", "room", room, "error", err)
		return text
	}
	// History returns the newest-page tail, not the delta itself (pages
	// crossing the boundary are how the recent window is reached). The delta
	// is the rows past the boundary; seen-coverage advances over everything
	// fetched, filtered rows included.
	boundary := st.PushSeenSeq
	for _, e := range entries {
		if e.Seq > st.PushSeenSeq {
			st.PushSeenSeq = e.Seq
		}
	}
	if boundary > 0 || !fresh {
		kept := entries[:0]
		for _, e := range entries {
			if e.Seq > boundary {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	entries = dropInstruction(entries, text)
	// The agent's own replies from THIS session are in its CLI memory: on a
	// follow-up they are dropped as redundancy (everything in the delta past
	// the last push is this-session by construction — the session existed when
	// it was said). On a fresh session the opposite holds: a rebind starts a
	// blank CLI, so its own PRIOR replies in the window are exactly the
	// continuity it needs and must stay.
	if fresh {
		boundary = noSelfBound
	}
	entries = dropSelf(entries, self, boundary)
	if fresh {
		return roomlog.Compose(entries, text, roomlog.Options{
			SelfSpeaker: self,
			WindowChars: b.windowChars,
			Summarizer:  b.summarizer,
			Attachments: atts,
		})
	}
	return roomlog.ComposeDelta(entries, text, roomlog.Options{
		SelfSpeaker: self,
		WindowChars: b.windowChars,
		Attachments: atts,
	})
}

// noSelfBound keeps every one of the agent's own replies (dropSelf): a fresh
// session's memory holds none of them.
const noSelfBound = int64(1) << 62

// dropInstruction removes the current instruction from the records: it is
// delivered as the [now] section, so a copy would both duplicate it and
// re-inject it as history on later turns. Only the newest matching entry
// goes — an older identical message is genuine history.
func dropInstruction(entries []roomlog.Entry, text string) []roomlog.Entry {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return entries
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if strings.TrimSpace(entries[i].Text) == trimmed {
			return append(entries[:i], entries[i+1:]...)
		}
	}
	return entries
}

// dropSelf removes the agent's own replies (speaker self) newer than bound:
// those were said in the current session, whose CLI memory holds them.
// Others' lines — people and other agents — always stay.
func dropSelf(entries []roomlog.Entry, self string, bound int64) []roomlog.Entry {
	if strings.TrimSpace(self) == "" {
		return entries
	}
	kept := entries[:0]
	for _, e := range entries {
		if e.Speaker == self && e.Seq > bound {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}
