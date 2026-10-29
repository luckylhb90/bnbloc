package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
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
	ID          string
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
		path := roomlog.AttachmentPath(id, contentType, filename)
		if err := w.WriteWorkspaceFileBytes(ctx, ep, workspaceID, path, blob); err != nil {
			miss(undeliveredRefused(err), err)
			continue
		}
		out.delivered = append(out.delivered, writtenAttachment{ID: id, Path: path, Filename: filename, ContentType: contentType})
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

// roomRecordFiles are the .room/ records of a pass besides the history
// (roomlog owns their wording): the session's guidance (README.md, rendered
// with its facts g) when the workspace does not hold its current text yet
// (roomlog.ReadmeUpdate: once, not every turn; readmeSum is what to record
// once written), the platform's contract files the same way (the app SDK
// in demo mode, its declarations and the content limits:
// roomlog.ContractsUpdate),
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
	// The platform's contracts as files: the app SDK in demo mode, for an
	// agent's self-test of an app it builds (served as the page's
	// ~app/sdk.js; nothing it does is sent), its declarations and the
	// content limits, which content refers to instead of copying.
	contracts, contractsSum, changed := roomlog.ContractsUpdate(st, roomlog.ContractFiles(appsdk.DemoJS, appsdk.Types, contentpkg.LimitsDoc()))
	next.ContractsSum = contractsSum
	if changed {
		files = append(files, contracts...)
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
	// The service components' files for this turn (turn_services.go).
	files = append(files, serviceFactFiles(b.jobFacts(ctx, room, jobID))...)
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
// channel session of the agent (docs/prd/2026-10-13-sessions-per-channel.md).
// A channel is a context boundary (docs/prd/2026-10-14-multi-member-rooms.md):
// every channel an agent may read keeps its own records in
// .room/channels/<id>/ (its recent.md and chunks), paged on its own, and
// INDEX.md lists the channels; no shared file depends on the turn's channel
// (the session guidance names it).
// channelID is the turn's channel ("" = the room's default). seen is the
// session's last seen sequence (0 = none): the own channel is read from
// there, so own holds every message of it the session has not seen. now is
// the current turn's message: its sender as the history records it is
// returned for the [now] marker. jobID is the turn's job, whose grant reads
// the requester's Library. self is this agent. Files are written channel
// records → records (roomRecordFiles) → INDEX (INDEX last, so a
// partial write never advertises files that do not exist). State is only
// advanced on full success, so a failed pass retries cleanly next turn.
func (b *Bridge) materializeRoomContext(ctx context.Context, w corepkg.WorkspaceFileWriter, ep corepkg.Endpoint, room, workspaceID, jobID, channelID string, seen, msgSeq int64, now string, self roomlog.Producer, st roomlog.CtxState, g roomlog.Guidance, cp *deliverableCopier) (next roomlog.CtxState, own []roomlog.Entry, from string, err error) {
	list, _ := roomlog.AgentChannelList(ctx, b.gatewayGET, room, jobID, channelID)
	ownCh, ok := roomlog.ChannelOf(list, channelID)
	if !ok {
		ownCh = list[0]
	}
	type read struct {
		ch       roomlog.Channel
		rec      roomlog.ChannelRecords
		entries  []roomlog.Entry
		complete bool
	}
	var reads []read
	var all []roomlog.Entry
	for _, ch := range list {
		rec := st.Channels[ch.ID]
		rec.Dir, rec.Name = roomlog.ChannelDir(ch.ID), ch.Name // the name follows a rename
		after := rec.EndSeq
		if ch.ID == ownCh.ID && seen > 0 && seen < after {
			after = seen
		}
		entries, complete, err := roomlog.History(ctx, b.gatewayGET, room, roomlog.HistoryQuery{Only: ch.ID, After: after, Job: jobID})
		if err != nil {
			return st, nil, "", err
		}
		reads = append(reads, read{ch: ch, rec: rec, entries: entries, complete: complete})
		all = append(all, entries...)
		if ch.ID == ownCh.ID {
			own = entries
		}
	}
	// People's lines carry their display names (by author id).
	names := map[string]string{}
	for _, e := range all {
		if e.Kind == roomlog.KindHuman {
			names[e.Speaker] = ""
		}
	}
	names = b.displayNames(ctx, slices.Collect(maps.Keys(names)))
	for i := range reads {
		reads[i].entries = roomlog.SetNames(reads[i].entries, names)
	}
	own = roomlog.SetNames(own, names)
	from = instructionSender(own, msgSeq, now)
	next = st
	if cp != nil {
		next = b.mirrorAttachments(ctx, room, all, next, cp)
	}
	next.Channels = map[string]roomlog.ChannelRecords{}
	var files []roomlog.NamedFile
	var index []roomlog.ChannelRecords
	next.Revised = nil
	for _, r := range reads {
		// Chunks holding an edited or deleted message are written again,
		// under their names: no file keeps the old text.
		for _, name := range roomlog.ChunksHolding(r.rec, st.Revised[r.ch.ID]) {
			first, last, _ := roomlog.ChunkRange(name)
			entries, _, err := roomlog.History(ctx, b.gatewayGET, room, roomlog.HistoryQuery{Only: r.ch.ID, After: first - 1, Before: last + 1, MaxRows: 2 * chunkRereadRows, Job: jobID})
			if err != nil {
				return st, nil, "", err
			}
			files = append(files, roomlog.RewriteChunk(name, roomlog.PointAttachments(b.nameEntries(ctx, entries), next.Attachments)))
		}
		entries := roomlog.PointAttachments(r.entries, next.Attachments)
		chFiles, rec := roomlog.MaterializeChannel(entries, r.complete, r.rec)
		files = append(files, chFiles...)
		next.Channels[r.ch.ID] = rec
		index = append(index, rec)
	}
	// A channel the previous pass wrote that this reader may not read any
	// more (left the listing) is blanked, so nothing it cannot read stays in
	// the workspace.
	for id, rec := range st.Channels {
		if _, kept := next.Channels[id]; !kept {
			files = append(files, roomlog.ForgetChannel(rec)...)
		}
	}
	records, recorded := b.roomRecordFiles(ctx, room, jobID, self, st, g, cp)
	next.ReadmeSum, next.AppsListed, next.ContractsSum = recorded.ReadmeSum, recorded.AppsListed, recorded.ContractsSum
	files = append(append(files, records...), roomlog.IndexFile(index))
	for _, f := range files {
		if err := w.WriteWorkspaceFile(ctx, ep, workspaceID, f.Name, f.Content); err != nil {
			return st, nil, "", err
		}
	}
	return next, own, from, nil
}

// chunkRereadRows bounds the rows a chunk rewrite reads (a chunk holds at
// most that many messages).
const chunkRereadRows = 200

// Revision reads: a page of the revisions listing, and the pages one look
// reads at most (later looks continue).
const (
	revisionPageRows = 200
	revisionPages    = 5
	// maxRevisedPerChannel bounds the sequences a channel's records wait
	// to rewrite.
	maxRevisedPerChannel = 500
)

// followRevisions brings st up to the room's edits and deletions since
// st.RevisedMs (docs/prd/2026-10-14-multi-member-rooms.md rule 2): their
// sequences, by channel, for the next pass to rewrite (Revised), and a
// deletion's channel for its sessions to reset (Resets). A workspace's
// first look only notes where the room stands: its records are written
// from the room as it is. Best-effort: a failed read is retried next time.
func (b *Bridge) followRevisions(ctx context.Context, room string, st roomlog.CtxState) roomlog.CtxState {
	first := st.RevisedMs == 0
	st.Revised, st.Resets, st.Expired = maps.Clone(st.Revised), maps.Clone(st.Resets), maps.Clone(st.Expired)
	now := time.Now().UnixMilli()
	for range revisionPages {
		raw, err := b.gatewayGET(ctx, "/v1/rooms/"+url.PathEscape(room)+"/history/revisions?after="+strconv.FormatInt(st.RevisedMs, 10)+"&limit="+strconv.Itoa(revisionPageRows))
		var page wire.RevisionsPage
		if err != nil || json.Unmarshal(raw, &page) != nil {
			return st
		}
		if page.RetainedFrom != nil {
			roomlog.ExpireBefore(&st, page.RetainedFrom)
		}
		if first {
			st.RevisedMs = max(page.LatestMs, 1) // 1: looked, nothing revised yet
			return st
		}
		for _, r := range page.Revisions {
			if st.Revised == nil {
				st.Revised = map[string][]int64{}
			}
			seqs := append(slices.Clone(st.Revised[r.ChannelID]), r.Sequence)
			st.Revised[r.ChannelID] = seqs[max(len(seqs)-maxRevisedPerChannel, 0):]
			if r.Deleted {
				if st.Resets == nil {
					st.Resets = map[string]int64{}
				}
				st.Resets[r.ChannelID] = now
			}
			st.RevisedMs = max(st.RevisedMs, r.AtMs)
		}
		if len(page.Revisions) < revisionPageRows {
			return st
		}
	}
	return st
}

// channelReset reports whether a reused session must start again before
// its turn: a message of its channel was deleted after it started, and the
// text may be in its memory (an edit only rewrites the records). The
// session code (ensureSession) unbinds such a session.
func (b *Bridge) channelReset(ctx context.Context, room, channelID string, rs *roomSession) bool {
	if rs.ws == nil {
		return false
	}
	rs.ws.pass.Lock()
	defer rs.ws.pass.Unlock()
	st := b.followRevisions(ctx, room, rs.ws.roomState())
	rs.ws.setRoomState(st)
	if len(st.Resets) == 0 {
		return false
	}
	if channelID == "" {
		list, _ := roomlog.AgentChannelList(ctx, b.gatewayGET, room, "", "")
		channelID = list[0].ID
	}
	return st.Resets[channelID] > rs.ctx.StartedMs
}

// guidanceChannel is the session's own channel's records directory, as
// its guidance names it (channelID "" = the room's default channel, as the
// pass resolves it).
func (b *Bridge) guidanceChannel(ctx context.Context, room, channelID string) string {
	if channelID == "" {
		list, _ := roomlog.AgentChannelList(ctx, b.gatewayGET, room, "", "")
		channelID = list[0].ID
	}
	return roomlog.GuidanceChannel(channelID)
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
	if fresh && st != nil {
		st.StartedMs = time.Now().UnixMilli()
	}
	now := roomlog.Now{From: b.personLabel(ctx, d.job.RequesterUserID), Text: text, Attachments: toPromptAttachments(atts.delivered), Undelivered: atts.undelivered, Edit: d.edit.line()}
	entry, ok := b.coreEntry(d.core)
	if d.caps.writer == nil || !ok {
		return b.driveTextDelta(ctx, room, owner, provider, d.job.JobID, d.job.MessageSeq, d.job.ChannelID, now, st, fresh)
	}
	w, ep := d.caps.writer, entry.endpoint
	// The tree is the workspace's: a session continues what the workspace
	// holds, whichever of the agent's sessions wrote it.
	shared := b.workspaces.shared(d.key(), workspaceID)
	// One pass at a time per workspace: the agent's channels' turns may run
	// at once, and each pass advances the tree's shared state.
	shared.pass.Lock()
	defer shared.pass.Unlock()
	var cp *deliverableCopier
	if d.caps.bytesWriter != nil {
		cp = &deliverableCopier{key: d.key(), workspaceID: workspaceID, w: d.caps.bytesWriter, ep: ep, maxInput: d.caps.maxInput}
	}
	roomSt := b.followRevisions(ctx, room, shared.roomState())
	roomSt.Attachments = maps.Clone(roomSt.Attachments)
	for _, a := range atts.delivered {
		if a.ID != "" {
			roomlog.Mirrored(&roomSt, a.ID, a.Path) // the turn's own files: never copied twice
		}
	}
	var seen int64
	if st != nil && !fresh {
		seen = st.PushSeenSeq
	}
	newSt, own, from, err := b.materializeRoomContext(ctx, w, ep, room, workspaceID, d.job.JobID, d.job.ChannelID, seen, d.job.MessageSeq, text, roomlog.Producer{Owner: owner, Provider: provider, Core: d.core}, roomSt, g, cp)
	if err != nil {
		slog.Warn("bnbloc bridge room-context materialization failed; falling back to pushed context",
			"event", "bnbloc.bridge.roomctx_failed", "room", room, "core", d.core, "error", err)
		return b.driveTextDelta(ctx, room, owner, provider, d.job.JobID, d.job.MessageSeq, d.job.ChannelID, now, st, fresh)
	}
	shared.setRoomState(newSt)
	b.workspaces.roomStateWritten(d.key(), workspaceID)
	if from != "" {
		now.From = from
	}
	// A new session of an agent that already took part in the channel's
	// conversation (its session was replaced) is handed it over: it
	// remembers none of it, its own replies included. An agent new to the
	// channel gets the pointer alone, and reads .room/ when it needs to.
	if fresh {
		if prior, _ := dropInstruction(own, d.job.MessageSeq, text); spoke(prior, agentSpeakerLabel(owner, provider)) {
			if st != nil {
				for _, e := range own {
					st.PushSeenSeq = max(st.PushSeenSeq, e.Seq)
				}
			}
			return roomlog.ComposeHandover(prior, b.guidanceChannel(ctx, room, d.job.ChannelID), now, !d.caps.profile, roomlog.Options{Summarizer: b.summarizer})
		}
	}
	// A reused session hears what others said in its channel since its last
	// turn, as conversation lines before the message (its own replies are
	// in its memory; a fresh session has .room/ and its guidance).
	var delta []roomlog.Entry
	if st != nil {
		delta = sinceLastTurn(own, seen, d.job.MessageSeq, text, agentSpeakerLabel(owner, provider))
		for _, e := range own {
			st.PushSeenSeq = max(st.PushSeenSeq, e.Seq)
		}
	}
	// A session whose guidance rode its first prompt (no agentProfile) keeps
	// one line pointing at the records: a harness may compact that prompt
	// away.
	return roomlog.ComposeTurn(now, !d.caps.profile, delta)
}

// sinceLastTurn is what others said in the session's channel after seen
// (0: nothing — a fresh session), without the turn's own message and the
// agent's own replies.
func sinceLastTurn(own []roomlog.Entry, seen, msgSeq int64, text, self string) []roomlog.Entry {
	if seen <= 0 {
		return nil
	}
	var out []roomlog.Entry
	for _, e := range own {
		if e.Seq > seen {
			out = append(out, e)
		}
	}
	out, _ = dropInstruction(out, msgSeq, text)
	return dropSelf(out, self, 0)
}

// deliverableCopier is where a pass copies the room's deliverables: one
// slot's workspace, through its core's bytes writer.
type deliverableCopier struct {
	key         driveKey
	workspaceID string
	w           corepkg.WorkspaceFileBytesWriter
	ep          corepkg.Endpoint
	// maxInput is the core's stated request limit (0 = unstated): a file
	// whose write would exceed it is not copied.
	maxInput int64
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

// mirrorAttachments copies the attachments of the pass's records the
// workspace does not hold yet into .room/attachments/ (roomlog's
// AttachmentsToMirror: newest first, bounded per file and pass), downloaded
// with the bridge's session as a member of the room, and records what it
// holds in the workspace state. A copy that fails is skipped (the record
// then names the file without a path) and tried again by a later pass.
func (b *Bridge) mirrorAttachments(ctx context.Context, room string, entries []roomlog.Entry, st roomlog.CtxState, cp *deliverableCopier) roomlog.CtxState {
	next := st
	next.Attachments = maps.Clone(st.Attachments)
	for _, a := range roomlog.AttachmentsToMirror(entries, st.Attachments) {
		if cp.maxInput > 0 && protocol.WriteFileBodyBytes(a.Size) > cp.maxInput {
			continue
		}
		blob, contentType, filename, err := b.gatewayGETAttachment(ctx, "/v1/rooms/"+room+"/attachments/"+a.ID)
		if err != nil || int64(len(blob)) > roomlog.MaxMirrorFileBytes {
			slog.Debug("bnbloc bridge attachment not mirrored", "event", "bnbloc.bridge.attachment_mirror_failed", "room", room, "attachment", a.ID, "error", err)
			continue
		}
		path := roomlog.AttachmentPath(a.ID, firstNonEmpty(a.ContentType, contentType), firstNonEmpty(a.Filename, filename))
		if err := cp.w.WriteWorkspaceFileBytes(ctx, cp.ep, cp.workspaceID, path, blob); err != nil {
			slog.Debug("bnbloc bridge attachment not mirrored", "event", "bnbloc.bridge.attachment_mirror_failed", "room", room, "attachment", a.ID, "error", err)
			continue
		}
		roomlog.Mirrored(&next, a.ID, path)
	}
	return next
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
func (b *Bridge) driveTextDelta(ctx context.Context, room, owner, provider, jobID string, msgSeq int64, channelID string, now roomlog.Now, st *roomlog.CtxState, fresh bool) string {
	if st == nil {
		return roomlog.ComposeDelta(nil, now, roomlog.Options{})
	}
	self := agentSpeakerLabel(owner, provider)
	// A channel is a context boundary: the session reads its own channel
	// (docs/prd/2026-10-14-multi-member-rooms.md).
	list, _ := roomlog.AgentChannelList(ctx, b.gatewayGET, room, jobID, channelID)
	own, ok := roomlog.ChannelOf(list, channelID)
	if !ok {
		own = list[0]
	}
	entries, _, err := roomlog.History(ctx, b.gatewayGET, room, roomlog.HistoryQuery{Only: own.ID, After: st.PushSeenSeq, MaxRows: b.recentRows, Job: jobID})
	if err != nil {
		slog.Debug("bnbloc bridge room-context unavailable, sending the message alone",
			"event", "bnbloc.bridge.context_unavailable", "room", room, "error", err)
		return roomlog.ComposeDelta(nil, now, roomlog.Options{})
	}
	entries = b.nameEntries(ctx, entries)
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
	entries, from := dropInstruction(entries, msgSeq, now.Text)
	if from != "" {
		now.From = from
	}
	// The agent's own replies from THIS session are in its CLI memory: on a
	// follow-up they are dropped as redundancy. On a fresh session the
	// opposite holds: a rebind starts a blank CLI, so its own PRIOR replies
	// in the window are exactly the continuity it needs and must stay.
	if fresh {
		boundary = noSelfBound
	}
	entries = dropSelf(entries, self, boundary)
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

// dropSelf removes the agent's own replies (speaker self) newer than bound:
// those were said in the current session, whose CLI memory holds them (the
// entries are the session's channel's).
// Others' lines — people and other agents — always stay.
// spoke reports whether self said something in entries.
func spoke(entries []roomlog.Entry, self string) bool {
	return strings.TrimSpace(self) != "" && slices.ContainsFunc(entries, func(e roomlog.Entry) bool { return e.Speaker == self })
}

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
