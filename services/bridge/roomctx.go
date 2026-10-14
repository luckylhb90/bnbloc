package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/gateway/appsdk"
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

// turnAttachments are a turn's message attachments: those written into the
// workspace and those that could not be (named in the prompt and in one
// notice to the room, never dropped silently).
type turnAttachments struct {
	delivered   []writtenAttachment
	undelivered []roomlog.UndeliveredAttachment
}

// Why an attachment did not reach the workspace, in words the agent and the
// member read.
const (
	undeliveredNoFiles  = "this agent's runtime cannot receive files"
	undeliveredDownload = "it could not be downloaded from the room"
)

// undeliveredTooLarge says a file is over the core's stated limit.
func undeliveredTooLarge(maxInput int64) string {
	return "too large for this agent's runtime (it accepts files up to " + roomlog.HumanBytes(protocol.MaxWriteFileBytes(maxInput)) + ")"
}

// undeliveredRefused says the core refused the write, with its own error
// cut short.
func undeliveredRefused(err error) string {
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	return "this agent's runtime refused it (" + msg + ")"
}

// materializeAttachments downloads each chat attachment and writes it under
// .room/attachments/<id><ext> so a binary-capable core can open it with its
// own file tools, and returns what was written (named in the turn's prompt)
// and what was not, with why: a core without the bytes writer, a file over
// the core's stated request limit (maxInput, 0 = unstated; never fetched), a
// failed download, or a write the core refused. Each failure is logged and
// the turn still runs.
func (b *Bridge) materializeAttachments(ctx context.Context, w corepkg.WorkspaceFileBytesWriter, maxInput int64, ep corepkg.Endpoint, room, workspaceID string, ids []string) turnAttachments {
	var out turnAttachments
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		meta := b.attachmentMeta(ctx, room, id)
		miss := func(reason string, err error) {
			slog.Warn("bnbloc bridge attachment not delivered", "event", "bnbloc.bridge.attachment_undelivered",
				"room", room, "attachment", id, "size", meta.Size, "reason", reason, "error", err)
			out.undelivered = append(out.undelivered, roomlog.UndeliveredAttachment{Filename: meta.Filename, ContentType: meta.ContentType, Size: meta.Size, Reason: reason})
		}
		if w == nil {
			miss(undeliveredNoFiles, nil)
			continue
		}
		if maxInput > 0 && meta.Size > 0 && protocol.WriteFileBodyBytes(meta.Size) > maxInput {
			miss(undeliveredTooLarge(maxInput), nil)
			continue
		}
		blob, contentType, filename, err := b.gatewayGETAttachment(ctx, "/v1/rooms/"+room+"/attachments/"+id)
		if err != nil {
			miss(undeliveredDownload, err)
			continue
		}
		meta.Size = int64(len(blob))
		meta.Filename = firstNonEmpty(meta.Filename, filename)
		meta.ContentType = firstNonEmpty(meta.ContentType, contentType)
		if maxInput > 0 && protocol.WriteFileBodyBytes(meta.Size) > maxInput {
			miss(undeliveredTooLarge(maxInput), nil)
			continue
		}
		path := ".room/attachments/" + id + extForContentType(contentType, filename)
		if err := w.WriteWorkspaceFileBytes(ctx, ep, workspaceID, path, blob); err != nil {
			miss(undeliveredRefused(err), err)
			continue
		}
		out.delivered = append(out.delivered, writtenAttachment{Path: path, Filename: filename, ContentType: contentType})
	}
	return out
}

// attachmentMeta reads an attachment's name, type and size from the room
// (zero when it cannot be read: the download still tries, and names it).
func (b *Bridge) attachmentMeta(ctx context.Context, room, id string) wire.ChatAttachment {
	var meta wire.ChatAttachment
	raw, err := b.gatewayGET(ctx, "/v1/rooms/"+room+"/attachments/"+id+"/meta")
	if err == nil {
		err = json.Unmarshal(raw, &meta)
	}
	if err != nil {
		slog.Debug("bnbloc bridge attachment meta unavailable", "event", "bnbloc.bridge.attachment_meta_failed", "room", room, "attachment", id, "error", err)
		return wire.ChatAttachment{}
	}
	return meta
}

// extForContentType is the extension an attachment is written with, so the
// agent's tools recognize the file (an image tool needs .png, a deck .pptx):
// its type's (wire.AttachmentTypes, which the upload held it to) — the
// original name's when that is one of them (.potx stays .potx) — else the
// original name's, else "".
func extForContentType(contentType, filename string) string {
	ext := ""
	if dot := strings.LastIndexByte(filename, '.'); dot >= 0 && dot+1 < len(filename) {
		ext = filename[dot:]
	}
	ct := strings.ToLower(strings.TrimSpace(contentType))
	for _, t := range wire.AttachmentTypes {
		if t.MIME != ct {
			continue
		}
		if slices.Contains(t.Extensions, strings.ToLower(ext)) {
			return strings.ToLower(ext)
		}
		return t.Extensions[0]
	}
	return ext
}

// roomRecordFiles are the .room/ records of a pass besides the history
// (roomlog owns their wording): the session's guidance (README.md, rendered
// with its facts g) when the workspace does not hold its current text yet
// (roomlog.ReadmeUpdate: once, not every turn; readmeSum is what to record
// once written), the app SDK in demo mode the same way
// (roomlog.AppSDKUpdate),
// then the data rewritten every turn: the deliverables with their producers
// (artifacts.md), the requester's Library (library.md, read with the job's
// grant) and the live interaction trail (live.md: what members did inside
// running deliverables since the bridge started, so a member's "the board
// looks wrong now" is answerable from the actual gestures), and, once the
// room has an interactive app, apps.md (roomlog.AppsUpdate: each app's
// collections and document counts, read with the job's grant; rewritten
// every turn from then on, so it never goes stale). A failed read renders
// as unreadable, never as empty, and never fails the turn. next is st with
// what the pass records (ReadmeSum, AppsListed) once the files are written.
func (b *Bridge) roomRecordFiles(ctx context.Context, room, jobID string, self roomlog.Producer, st roomlog.CtxState, g roomlog.Guidance, cp *deliverableCopier) (files []roomlog.NamedFile, next roomlog.CtxState) {
	next = st
	readme, readmeSum, changed := roomlog.ReadmeUpdate(st, g)
	next.ReadmeSum = readmeSum
	if changed {
		files = append(files, readme)
	}
	// The app SDK in demo mode, for an agent's self-test of an app it
	// builds (served as the page's ~app/sdk.js; nothing it does is sent).
	sdk, sdkSum, changed := roomlog.AppSDKUpdate(st, appsdk.DemoJS)
	next.AppSDKSum = sdkSum
	if changed {
		files = append(files, sdk)
	}
	apps, aerr := roomlog.ReadApps(ctx, b.gatewayGET, room, jobID, &b.producers)
	if aerr != nil {
		slog.Debug("bnbloc bridge apps unreadable", "event", "bnbloc.bridge.roomctx_apps_failed", "room", room, "job", jobID, "error", aerr)
	}
	appsFile, writeApps, appsListed := roomlog.AppsUpdate(st.AppsListed, apps, aerr)
	next.AppsListed = appsListed
	deliverables, derr := roomlog.ReadDeliverables(ctx, b.gatewayGET, room, &b.producers)
	if derr != nil {
		slog.Debug("bnbloc bridge deliverables unreadable", "event", "bnbloc.bridge.roomctx_deliverables_failed", "room", room, "error", derr)
	}
	// The deliverables' copies are written before the records that name
	// them (artifacts.md, INDEX last), so a record never points at a copy
	// that is not there.
	var copies map[string]roomlog.Copy
	if derr == nil && cp != nil {
		copies = b.copyDeliverables(ctx, room, deliverables, cp)
	}
	library, lerr := roomlog.ReadLibrary(ctx, b.gatewayGET, room, jobID, "")
	if lerr != nil {
		slog.Debug("bnbloc bridge library unreadable", "event", "bnbloc.bridge.roomctx_library_failed", "room", room, "job", jobID, "error", lerr)
	}
	files = append(files,
		roomlog.DeliverablesFile(deliverables, derr, self, copies),
		roomlog.LibraryFile(library, lerr),
		roomlog.LiveFile(b.liveTrail(room)),
	)
	if writeApps {
		files = append(files, appsFile)
	}
	return files, next
}

// materializeRoomContext refreshes the .room/ tree in the given workspace and
// returns the updated state. The tree is the workspace's, shared by every
// channel session of the agent (docs/prd/2026-10-13-sessions-per-channel.md):
// it holds the room's records, every line of every channel an agent may
// read, the agent's own replies and the current message included (a filter
// of one session would hide them from another session, whose memory does
// not hold them; the turn answers its [now] message alone, records are
// background). now is the current turn's message: its sender as the
// history records it (roomlog.Sender: "jack/codex for maya" for a
// dispatched task; "" when the fetch did not hold it) is returned for the
// [now] marker. jobID is the turn's job, whose grant reads the requester's
// Library. self is this agent. Files are written chunks → recent → records
// (roomRecordFiles) → INDEX (INDEX last, so a partial write never
// advertises files that do not exist). State is only advanced on full
// success, so a failed pass retries cleanly next turn.
func (b *Bridge) materializeRoomContext(ctx context.Context, w corepkg.WorkspaceFileWriter, ep corepkg.Endpoint, room, workspaceID, jobID string, msgSeq int64, now string, self roomlog.Producer, st roomlog.CtxState, g roomlog.Guidance, cp *deliverableCopier) (roomlog.CtxState, string, error) {
	entries, complete, err := roomlog.History(ctx, b.gatewayGET, room, roomlog.HistoryQuery{After: st.EndSeq})
	if err != nil {
		return st, "", err
	}
	from := instructionSender(entries, msgSeq, now)
	files, newState := roomlog.Materialize(entries, complete, st)
	// Insert the records before the trailing INDEX.
	records, recorded := b.roomRecordFiles(ctx, room, jobID, self, st, g, cp)
	newState.ReadmeSum, newState.AppsListed, newState.AppSDKSum = recorded.ReadmeSum, recorded.AppsListed, recorded.AppSDKSum
	if n := len(files); n > 0 && files[n-1].Name == ".room/INDEX.md" {
		files = append(append(files[:n-1:n-1], records...), files[n-1])
	} else {
		files = append(files, records...)
	}
	for _, f := range files {
		if err := w.WriteWorkspaceFile(ctx, ep, workspaceID, f.Name, f.Content); err != nil {
			return st, "", err
		}
	}
	return newState, from, nil
}

// drivePrompt builds a turn's prompt. A pull-capable core gets the pointer
// prompt over the workspace's materialized .room/ files (a fresh session's
// first pass runs before the session is created — the workspace exists, the
// session does not yet); any materialization failure falls back to the push
// model for this turn. A core without the writer capability uses the push
// model every turn, with the session's own push state st. The [now] marker
// names the message's sender as the room history records it, else the
// job's requester.
func (b *Bridge) drivePrompt(ctx context.Context, d *drive, workspaceID string, atts turnAttachments, st *roomlog.CtxState, g roomlog.Guidance, fresh bool) string {
	room, owner, provider, text := d.job.RoomID, d.owner, d.job.Provider, d.text
	now := roomlog.Now{From: d.job.RequesterUserID, Text: text, Attachments: toPromptAttachments(atts.delivered), Undelivered: atts.undelivered}
	entry, ok := b.coreEntry(d.core)
	if d.caps.writer == nil || !ok {
		return b.driveTextDelta(ctx, room, owner, provider, d.job.MessageSeq, d.job.ChannelID, now, st, fresh)
	}
	w, ep := d.caps.writer, entry.endpoint
	// The tree is the workspace's: a session continues what the workspace
	// holds, whichever of the agent's sessions wrote it.
	shared := b.workspaces.shared(d.key(), workspaceID)
	var cp *deliverableCopier
	if d.caps.bytesWriter != nil {
		cp = &deliverableCopier{key: d.key(), workspaceID: workspaceID, w: d.caps.bytesWriter, ep: ep}
	}
	newSt, from, err := b.materializeRoomContext(ctx, w, ep, room, workspaceID, d.job.JobID, d.job.MessageSeq, text, roomlog.Producer{Owner: owner, Provider: provider, Core: d.core}, shared.roomState(), g, cp)
	if err != nil {
		slog.Warn("bnbloc bridge room-context materialization failed; falling back to pushed context",
			"event", "bnbloc.bridge.roomctx_failed", "room", room, "core", d.core, "error", err)
		return b.driveTextDelta(ctx, room, owner, provider, d.job.MessageSeq, d.job.ChannelID, now, st, fresh)
	}
	shared.setRoomState(newSt)
	if from != "" {
		now.From = from
	}
	// A session whose guidance rode its first prompt (no agentProfile) keeps
	// one line pointing at the records: a harness may compact that prompt
	// away.
	return roomlog.ComposePointer(now, !d.caps.profile)
}

// deliverableCopier is where a pass copies the room's deliverables: one
// slot's workspace, through its core's bytes writer.
type deliverableCopier struct {
	key         driveKey
	workspaceID string
	w           corepkg.WorkspaceFileBytesWriter
	ep          corepkg.Endpoint
}

// copyDeliverables writes the listed deliverables whose latest version the
// workspace does not hold yet into its .room/deliverables/
// (roomlog.CopyDeliverables) and records what it holds now with the
// workspace, which outlives the session (agentWorkspaces).
func (b *Bridge) copyDeliverables(ctx context.Context, room string, list []roomlog.Deliverable, cp *deliverableCopier) map[string]roomlog.Copy {
	have := b.workspaces.copies(cp.key, cp.workspaceID)
	copies, next := roomlog.CopyDeliverables(ctx, b.gatewayGET, room, list, have, func(ctx context.Context, name string, data []byte) error {
		return cp.w.WriteWorkspaceFileBytes(ctx, cp.ep, cp.workspaceID, name, data)
	})
	b.workspaces.setCopies(cp.key, cp.workspaceID, next)
	return copies
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
// authored the self replies, and the instruction is delivered as [now]
// (now.From is the sender when the history does not name it).
func (b *Bridge) driveTextDelta(ctx context.Context, room, owner, provider string, msgSeq int64, channelID string, now roomlog.Now, st *roomlog.CtxState, fresh bool) string {
	if st == nil {
		return roomlog.ComposeDelta(nil, now, roomlog.Options{})
	}
	self := agentSpeakerLabel(owner, provider)
	entries, _, err := roomlog.History(ctx, b.gatewayGET, room, roomlog.HistoryQuery{After: st.PushSeenSeq, MaxRows: b.recentRows})
	if err != nil {
		slog.Debug("bnbloc bridge room-context unavailable, sending the message alone",
			"event", "bnbloc.bridge.context_unavailable", "room", room, "error", err)
		return roomlog.ComposeDelta(nil, now, roomlog.Options{})
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
	// The turn's channel: the instruction's as the history records it (the
	// job names the default channel ""), else the job's.
	channel := channelID
	if e, ok := instructionEntry(entries, msgSeq, now.Text); ok && e.ChannelID != "" {
		channel = e.ChannelID
	}
	entries, from := dropInstruction(entries, msgSeq, now.Text)
	if from != "" {
		now.From = from
	}
	// The agent's own replies from THIS session are in its CLI memory: on a
	// follow-up they are dropped as redundancy — the ones past the last push
	// in this session's channel (a session is one channel's: its replies in
	// the agent's other channels are not in its memory). On a fresh session
	// the opposite holds: a rebind starts a blank CLI, so its own PRIOR
	// replies in the window are exactly the continuity it needs and must
	// stay.
	if fresh {
		boundary = noSelfBound
	}
	entries = roomlog.MarkChannels(dropSelf(entries, self, boundary, channel), channel)
	if fresh {
		return roomlog.Compose(entries, now, roomlog.Options{
			WindowChars: b.windowChars,
			Summarizer:  b.summarizer,
		})
	}
	return roomlog.ComposeDelta(entries, now, roomlog.Options{
		WindowChars: b.windowChars,
	})
}

// noSelfBound keeps every one of the agent's own replies (dropSelf): a fresh
// session's memory holds none of them.
const noSelfBound = int64(1) << 62

// instructionSender is the sender of the current instruction as the
// history records it ("" when the history does not hold it).
func instructionSender(entries []roomlog.Entry, seq int64, text string) string {
	if e, ok := instructionEntry(entries, seq, text); ok {
		return roomlog.Sender(e)
	}
	return ""
}

// instructionEntry finds the current instruction in the records: the
// person's message at the job's sequence. Only a job without one (seq 0)
// falls back to the newest message with its text — two people can send
// the same words, and a match by text would name the wrong one.
func instructionEntry(entries []roomlog.Entry, seq int64, text string) (roomlog.Entry, bool) {
	i := instructionIndex(entries, seq, text)
	if i < 0 {
		return roomlog.Entry{}, false
	}
	return entries[i], true
}

func instructionIndex(entries []roomlog.Entry, seq int64, text string) int {
	if seq > 0 {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Seq == seq && entries[i].Kind == roomlog.KindHuman {
				return i
			}
		}
		return -1
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return -1
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if strings.TrimSpace(entries[i].Text) == trimmed {
			return i
		}
	}
	return -1
}

// dropInstruction removes the current instruction from the records: it is
// delivered as the [now] section, so a copy would both duplicate it and
// re-inject it as history on later turns. from is its sender
// (roomlog.Sender), "" when the records do not hold it.
func dropInstruction(entries []roomlog.Entry, seq int64, text string) (kept []roomlog.Entry, from string) {
	i := instructionIndex(entries, seq, text)
	if i < 0 {
		return entries, ""
	}
	from = roomlog.Sender(entries[i])
	return append(entries[:i], entries[i+1:]...), from
}

// dropSelf removes the agent's own replies (speaker self) newer than bound
// in the session's channel: those were said in the current session, whose
// CLI memory holds them.
// Others' lines — people and other agents — always stay.
func dropSelf(entries []roomlog.Entry, self string, bound int64, channel string) []roomlog.Entry {
	if strings.TrimSpace(self) == "" {
		return entries
	}
	kept := entries[:0]
	for _, e := range entries {
		if e.Speaker == self && e.Seq > bound && (channel == "" || e.ChannelID == "" || e.ChannelID == channel) {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}
