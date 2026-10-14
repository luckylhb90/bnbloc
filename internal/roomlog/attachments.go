package roomlog

import (
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room's attachments are the room's (docs/prd/2026-10-14-multi-member-rooms.md
// rule 4): a file a person attached reaches every agent of the room as a
// file in its workspace, not only the agent the message addressed. Each
// pass mirrors the attachments of the lines it records into
// .room/attachments/ — lazily, newest first, bounded per file and per pass
// — and the record line points at the copy. The turn's own attachments are
// written to the same path, so a file is never copied twice.

// Mirror bounds: the largest attachment a pass copies — any chat
// attachment (wire.MaxAttachmentBytes), so an agent can read an earlier
// message's file however large the room allowed it; a core that takes less
// states its limit and the bridge skips what exceeds it — and what one pass
// copies at most (later passes continue, newest first).
const (
	MaxMirrorFileBytes = wire.MaxAttachmentBytes
	MaxMirrorPassBytes = 32 << 20
	// maxMirrorPassFiles is the most files one pass copies.
	maxMirrorPassFiles = 20
	// maxMirrored bounds the copies a workspace remembers.
	maxMirrored = 2000
)

// AttachmentPath is where an attachment lives in a workspace:
// .room/attachments/<id><ext>. The extension lets the agent's tools
// recognize the file (an image tool needs .png, a deck .pptx): its type's
// (wire.AttachmentTypes, which the upload held it to) — the original name's
// when that is one of them (.potx stays .potx) — else the original name's,
// else none.
func AttachmentPath(id, contentType, filename string) string {
	return ".room/attachments/" + id + attachmentExt(contentType, filename)
}

func attachmentExt(contentType, filename string) string {
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

// AttachmentsToMirror picks the attachments of entries a pass copies: the
// ones the workspace does not hold yet (mirrored: id → path), newest first,
// none over MaxMirrorFileBytes, at most maxMirrorPassFiles and
// MaxMirrorPassBytes a pass. Every attachment is named in the records
// whether copied or not (its id: read_artifact reads it).
func AttachmentsToMirror(entries []Entry, mirrored map[string]string) []AttachmentRef {
	var out []AttachmentRef
	var budget int64 = MaxMirrorPassBytes
	seen := map[string]bool{}
	for i := len(entries) - 1; i >= 0; i-- {
		for _, a := range entries[i].Attachments {
			if a.ID == "" || seen[a.ID] || mirrored[a.ID] != "" || a.Size > MaxMirrorFileBytes || a.Size > budget {
				continue
			}
			seen[a.ID] = true
			budget -= a.Size
			if out = append(out, a); len(out) >= maxMirrorPassFiles {
				return out
			}
		}
	}
	return out
}

// PointAttachments sets each attachment's workspace path from mirrored
// (id → path), so its record line points at the copy.
func PointAttachments(entries []Entry, mirrored map[string]string) []Entry {
	for i := range entries {
		for j := range entries[i].Attachments {
			if p := mirrored[entries[i].Attachments[j].ID]; p != "" {
				entries[i].Attachments[j].Path = p
			}
		}
	}
	return entries
}

// Mirrored records copies in a workspace's state, forgetting the oldest
// half when the map grows past its bound (a forgotten copy is copied again
// when a later record shows it).
func Mirrored(st *CtxState, id, path string) {
	if st.Attachments == nil {
		st.Attachments = map[string]string{}
	}
	if len(st.Attachments) >= maxMirrored {
		st.Attachments = map[string]string{}
	}
	st.Attachments[id] = path
}
