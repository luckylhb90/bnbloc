package roomtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// read_attachment (docs/prd/2026-10-14-multi-member-rooms.md, rule 4): a
// room's attachments are the room's. Only the agent a message addressed
// gets its files in its workspace; any other agent of the room reads one
// here by the id its record line shows ([attached: … id att_…]). An agent
// reads only what its records show: the attachment must belong to a
// message of a channel an agent may read (roomlog.History never reads the
// people chat), so a file shared among people only never reaches one. The
// download goes through the room's own attachment route with the bridge's
// session, so realtime applies its membership check. Text comes back
// inline, bounded and fenced as data its uploader wrote; any other file is
// written into the session's workspace (.room/attachments/) when its core
// takes files, and its path returned.

// read_attachment bounds.
const (
	// maxAttachmentText is the most text it returns inline.
	maxAttachmentText = 64 << 10
	// maxAttachmentFetch is the largest attachment it downloads (the
	// upload cap: wire.MaxAttachmentBytes).
	maxAttachmentFetch = wire.MaxAttachmentBytes
)

// WorkspaceWriter writes a file into the session's workspace (a core that
// takes binary files); a Workspace may implement it.
type WorkspaceWriter interface {
	WriteFile(ctx context.Context, rel string, data []byte) error
}

func readAttachmentTool() Tool {
	return Tool{
		Name:  "read_attachment",
		Title: "Read a file someone attached",
		Description: "Read a file a person attached to a message in this room, by the id its record line shows ([attached: … id att_…]). " +
			"Text comes back inline, fenced as data its uploader wrote; any other file is saved into your workspace under .room/attachments/ and its path returned.",
		InputSchema: objectSchema(map[string]any{
			"attachmentId": map[string]any{"type": "string", "description": "The attachment's id (att_…)."},
		}, "attachmentId"),
		ReadOnly: true,
		Call:     readAttachment,
	}
}

func readAttachment(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in struct {
		AttachmentID string `json:"attachmentId"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	if env.Session.current().JobID == "" {
		return toolError("read_attachment works only while you are answering a request in the room")
	}
	id := strings.TrimSpace(in.AttachmentID)
	if id == "" || strings.ContainsAny(id, "/?#") {
		return toolError("attachmentId must be an attachment id (att_…)")
	}
	room := env.Session.Scope().RoomID
	// The records' boundary: an attachment of a message an agent may read.
	entries, _, err := roomlog.History(ctx, gatewayFetch(env), room, roomlog.HistoryQuery{})
	if err != nil {
		return refusal("reading the room", err)
	}
	var ref *roomlog.AttachmentRef
	var uploader string
	for _, e := range entries {
		for i := range e.Attachments {
			if e.Attachments[i].ID == id {
				ref, uploader = &e.Attachments[i], e.Speaker
			}
		}
	}
	if ref == nil {
		return toolError("%s is not a file attached to a message you can read in this room", id)
	}
	out := map[string]any{"attachmentId": id, "filename": ref.Filename, "contentType": ref.ContentType, "sizeBytes": ref.Size, "attachedBy": uploader}
	if ref.Size > maxAttachmentFetch {
		out["note"] = "larger than read_attachment reads: ask the person to open it in the room"
		return Result{Value: out}
	}
	data, err := env.Gateway.Do(ctx, http.MethodGet, "/v1/rooms/"+url.PathEscape(room)+"/attachments/"+url.PathEscape(id), nil)
	if err != nil {
		return refusal("reading attachment "+id, err)
	}
	if textual(ref.ContentType) && utf8.Valid(data) {
		text := string(data)
		if len(text) > maxAttachmentText {
			text = roomlog.Truncate(text, maxAttachmentText)
			out["truncated"] = true
		}
		out["content"] = roomlog.Fence(uploader, text)
		return Result{Value: out}
	}
	w, ok := env.Session.workspace().(WorkspaceWriter)
	if !ok || w == nil {
		out["note"] = "not text, and this agent's core takes no files: ask the person to share its content"
		return Result{Value: out}
	}
	rel := ".room/attachments/" + id + attachmentExt(ref.Filename)
	if err := w.WriteFile(ctx, rel, data); err != nil {
		return toolError("saving %s into your workspace failed: %v", id, err)
	}
	out["path"] = rel
	out["note"] = "saved into your workspace: open it with your own tools"
	return Result{Value: out}
}

// textual reports a content type read inline as text.
func textual(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return strings.HasPrefix(ct, "text/") || ct == "application/json" || ct == "application/yaml"
}

// attachmentExt is the file's own extension (lower case, letters and
// digits only), so the agent's tools recognize it; "" when it has none.
func attachmentExt(name string) string {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(name)))
	if len(ext) < 2 || len(ext) > 10 {
		return ""
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}
