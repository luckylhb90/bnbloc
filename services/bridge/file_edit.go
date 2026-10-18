package bridge

// A request that edits a room file (docs/prd/2026-10-14-ai-document-editing.md
// rules 3 and 4). The member asked from the file's Ask AI panel, so the
// job names the file (job.FileRef). Before the turn the bridge places that
// version of the file under .room/edits/<id>/ (bnbloc writes only its own
// paths: core.CheckWorkspaceFile). The turn input gets one line naming it
// and the path to save the agent's version at (roomlog.EditTarget):
//
//   - for the agent's own file, its own path;
//   - for any other file, edits/<name>.
//
// Saving the file there publishes the next version of THAT artifact,
// whatever the path, the ignore rules or the per-pass cap say. The version
// records editedFrom, the version the member was looking at. A turn that
// leaves it as the version was says so in the room ("No changes to
// deck.pptx").

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// editsDir is where a file that is not the agent's own is placed.
const editsDir = "edits"

// editTarget is the room file a turn edits: which artifact and version,
// where it was placed (or why it was not), and what the publish passes did
// with it.
type editTarget struct {
	artifactID string
	version    int
	name       string
	// source is where the version was placed; path is where the agent's
	// version is saved and published from.
	source string
	path   string
	// problem says why the file is not in the workspace ("" = it is).
	problem string
	// selection is the part the member points at (nil = the whole file).
	selection *wire.FileSelection

	mu sync.Mutex
	// hash is the content last placed or published: a pass publishes only
	// a change from it.
	hash string
	// published is the version the turn's edit became (0 = none yet).
	published int
}

// line is the turn input's edit line.
func (e *editTarget) line() *roomlog.EditTarget {
	if e == nil {
		return nil
	}
	t := &roomlog.EditTarget{Path: e.path, Source: e.source, Name: e.name, Version: e.version, Problem: e.problem}
	if e.selection != nil {
		t.SelectionLabel, t.SelectionDetail, t.SelectionMask = e.selection.Label, e.selection.Detail, e.selection.Mask
	}
	return t
}

func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// editFileName is a file's name as a single safe path segment.
func editFileName(art wire.Artifact) string {
	name := path.Base(firstNonEmpty(art.FileName, art.Title, art.ArtifactID))
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 0x20 {
			return '_'
		}
		return r
	}, name)
	// One segment, never hidden (a dot file is not a deliverable) and never
	// a drive or "..": core.CleanRelPath owns that rule.
	if clean, ok := corepkg.CleanRelPath(name); !ok || clean != name || strings.HasPrefix(name, ".") || strings.Contains(name, ":") {
		name = "file-" + strings.NewReplacer(".", "_", ":", "_").Replace(name)
	}
	return name
}

// editPath is where the agent's version of the file is saved: the agent's
// own file at its own path (where it works on it anyway), any other under
// edits/ by its file name.
func editPath(room string, seat driveKey, core, provider, owner string, art wire.Artifact) string {
	if strings.HasPrefix(art.ArtifactID, "artf_") && art.Title != "" && artifactIDForSeat(room, seat, core, provider, owner, art.Title) == art.ArtifactID {
		return art.Title
	}
	return editsDir + "/" + editFileName(art)
}

// placeEditTarget puts the version the job names into the workspace (nil
// for a job that names none). A file that cannot be placed is still a
// target: its problem goes to the agent and to the room.
func (b *Bridge) placeEditTarget(ctx context.Context, d *drive, seat driveKey, ep corepkg.Endpoint, workspaceID string) *editTarget {
	ref := d.job.FileRef
	if ref == nil || ref.ArtifactID == "" {
		return nil
	}
	room := d.job.RoomID
	e := &editTarget{artifactID: ref.ArtifactID, version: ref.Version, name: firstNonEmpty(ref.Name, ref.ArtifactID), selection: ref.Selection}
	miss := func(problem string, err error) *editTarget {
		e.problem = problem
		slog.Warn("bnbloc bridge file to edit not placed", "event", "bnbloc.bridge.edit_unplaced",
			"room", room, "artifact", ref.ArtifactID, "version", ref.Version, "reason", problem, "error", err)
		return e
	}
	raw, err := b.gatewayGET(ctx, "/v1/rooms/"+url.PathEscape(room)+"/artifacts/"+url.PathEscape(ref.ArtifactID))
	if err != nil {
		return miss("it could not be read from the room", err)
	}
	var detail wire.ArtifactDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return miss("it could not be read from the room", err)
	}
	e.path = editPath(room, seat, d.core, d.job.Provider, d.owner, detail.Artifact)
	e.source = roomlog.EditsDir + editFileSegment(ref.ArtifactID) + "/" + editFileName(detail.Artifact)
	w := d.caps.bytesWriter
	if w == nil {
		return miss(undeliveredNoFiles, nil)
	}
	data, _, _, err := b.gatewayGETAttachment(ctx, fmt.Sprintf("/v1/rooms/%s/artifacts/%s/versions/%d/content", url.PathEscape(room), url.PathEscape(ref.ArtifactID), ref.Version))
	if err != nil {
		return miss(undeliveredDownload, err)
	}
	if max := d.caps.maxInput; max > 0 && protocol.WriteFileBodyBytes(int64(len(data))) > max {
		return miss(undeliveredTooLarge(max), nil)
	}
	if err := w.WriteWorkspaceFileBytes(ctx, ep, workspaceID, e.source, data); err != nil {
		return miss(undeliveredRefused(err), err)
	}
	// The version as placed: a save that is still these bytes is no change.
	e.hash = contentHash(data)
	b.workspaces.shared(seat, workspaceID).rememberEdit(e.path, e.artifactID)
	return e
}

// publishEdit publishes the edit target when the workspace's copy changed
// since it was placed or last published: the next version of the target
// artifact, recording the version the edit started from. It runs in every
// publish pass of the turn (the caller holds the workspace's publish lock).
func (b *Bridge) publishEdit(ctx context.Context, room, channelID string, rs *roomSession, files corepkg.SessionFiles, ep corepkg.Endpoint, binarySafe bool, current []corepkg.SessionFile, turn deliverableTurn) {
	e := turn.edit
	if e == nil || e.problem != "" || e.path == "" {
		return
	}
	// Only a save during this turn is the agent's edit: a copy left at the
	// path by an earlier edit is not.
	const skewMs = 2000
	found := false
	for _, f := range current {
		if f.Path == e.path {
			found = f.MtimeMs >= turn.StartMs-skewMs
			break
		}
	}
	if !found {
		return
	}
	content, err := files.ReadSessionFile(ctx, ep, rs.workspaceID, rs.sessionID, e.path)
	if err != nil {
		slog.Debug("bnbloc bridge edited file unreadable", "event", "bnbloc.bridge.edit_read_failed", "room", room, "path", e.path, "error", err)
		return
	}
	h := contentHash(content)
	e.mu.Lock()
	unchanged := h == e.hash
	e.mu.Unlock()
	if unchanged {
		return
	}
	maxBytes, err := b.artifactMaxBytes(ctx, room)
	if err != nil {
		return // the next pass retries
	}
	d, why := deliverableFor(e.path, content, binarySafe, maxBytes, turn)
	if why != "" {
		slog.Warn("bnbloc bridge edited file not publishable", "event", "bnbloc.bridge.edit_unpublishable", "room", room, "path", e.path, "reason", why)
		return
	}
	d.artifactID, d.editedFrom, d.fileName = e.artifactID, e.version, e.name
	version, err := b.publishTo(ctx, room, channelID, rs, d, "agent")
	if err != nil {
		slog.Warn("bnbloc bridge edit publish failed", "event", "bnbloc.bridge.edit_publish_failed", "room", room, "artifact", e.artifactID, "error", err)
		return
	}
	e.mu.Lock()
	e.hash = h
	if version > 0 {
		e.published = version
	}
	e.mu.Unlock()
}

// noticeEditOutcome tells the room, once the turn's publishing is done,
// when the file the member asked about did not change: it was never
// placed (and why), or the agent left it as it was.
func (b *Bridge) noticeEditOutcome(ctx context.Context, d *drive) {
	e := d.edit
	if e == nil {
		return
	}
	e.mu.Lock()
	published := e.published
	e.mu.Unlock()
	if published > 0 {
		return
	}
	text := "No changes to " + e.name + "."
	if e.problem != "" {
		text = fmt.Sprintf("Could not give %s %s v%d to edit: %s.", d.job.Provider, e.name, e.version, e.problem)
	}
	job := d.ref()
	n := wire.JobNotice{
		Kind: wire.NoticeFileEdit, Text: text,
		OwnerUserID: d.owner, Provider: d.job.Provider,
		RoomID: job.RoomID, ChannelID: job.ChannelID, JobID: job.ID, SourceSeq: job.MessageSeq,
		Session: wire.FileEditNoticeSessionID(job.ID), AtUnixMs: time.Now().UnixMilli(),
	}
	if _, err := b.gatewayPOST(ctx, "/v1/rooms/"+job.RoomID+"/agent-activity/messages", n.Report()); err != nil {
		slog.Warn("bnbloc bridge post notice failed", "event", "bnbloc.bridge.post_failed", "room", job.RoomID, "kind", n.Kind, "error", err)
	}
}

// editFileSegment is an artifact id as one path segment.
func editFileSegment(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '.' || r < 0x20 {
			return '_'
		}
		return r
	}, id)
}
