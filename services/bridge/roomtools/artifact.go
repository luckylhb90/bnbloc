package roomtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// read_artifact (docs/bugs/2026-10-12-agent-misreads-other-agents-work.md):
// an agent reads a room deliverable another agent published, instead of
// looking for it in its own workspace, where it never is. Every read goes
// through the room's own artifact routes with the bridge's session, so the
// room service applies its membership and room checks (a non-member, or an
// artifact of another room, is refused there); the tool reads only the
// session's room. A site's file comes through the gateway's site file read
// with the bound job's grant, which the room service checks the same way. The text comes back bounded and fenced as untrusted data
// written by someone else.
//
// It reads a room attachment the same way, by its id (att_…; the records
// point at it where .room/attachments/ holds no copy): a room's
// attachments are the room's (docs/prd/2026-10-14-multi-member-rooms.md
// §2b), and the room service checks membership on its meta and download
// routes as it does for deliverables.

// read_artifact bounds.
const (
	// maxArtifactFetchBytes is the largest stored version the tool
	// downloads. A site is read one file at a time, which the gateway's
	// site file read bounds.
	maxArtifactFetchBytes = 4 << 20
	// maxArtifactText is the most text it returns.
	maxArtifactText = 64 << 10
)

func readArtifactTool() Tool {
	return Tool{
		Name:  "read_artifact",
		Title: "Read a room deliverable or attachment",
		Description: "Read the text of one of this room's deliverables (a file an agent published, or a person uploaded) or attachments " +
			"by its id, from ask_agent, room_status, .room/artifacts.md or the records. version defaults to the latest; " +
			"path picks a file of a site deliverable (default index.html). The content comes fenced as data its producer wrote.",
		InputSchema: objectSchema(map[string]any{
			"artifactId": map[string]any{"type": "string", "description": "The deliverable's id (art_… or artf_…) or an attachment's (att_…)."},
			"path":       map[string]any{"type": "string", "description": "For a site deliverable: the file to read, e.g. index.html."},
			"version":    map[string]any{"type": "integer", "minimum": 1, "description": "A version (default: the latest)."},
		}, "artifactId"),
		ReadOnly: true,
		Call:     readArtifact,
	}
}

func readArtifact(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in struct {
		ArtifactID string `json:"artifactId"`
		Path       string `json:"path"`
		Version    int    `json:"version"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	if env.Session.current().JobID == "" {
		return toolError("read_artifact works only while you are answering a request in the room")
	}
	id := strings.TrimSpace(in.ArtifactID)
	if id == "" || strings.ContainsAny(id, "/?#") {
		return toolError("artifactId must be a deliverable id (art_… or artf_…) or an attachment id (att_…)")
	}
	room := env.Session.Scope().RoomID
	if strings.HasPrefix(id, attachmentIDPrefix) {
		if strings.TrimSpace(in.Path) != "" || in.Version > 0 {
			return toolError("path and version apply only to a deliverable; %s is an attachment", id)
		}
		return readAttachment(ctx, env, room, id)
	}
	base := "/v1/rooms/" + url.PathEscape(room) + "/artifacts/" + url.PathEscape(id)
	raw, err := env.Gateway.Do(ctx, http.MethodGet, base, nil)
	if err != nil {
		return refusal("reading deliverable "+id, err)
	}
	var detail wire.ArtifactDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return failure("unreadable deliverable %s: %v", id, err)
	}
	art := detail.Artifact
	if art.RoomID != room {
		return toolError("deliverable %s is not one of this room's", id)
	}
	version := in.Version
	if version <= 0 {
		version = art.CurrentVersion
	}
	raw, err = env.Gateway.Do(ctx, http.MethodGet, base+"/versions/"+strconv.Itoa(version), nil)
	if err != nil {
		return refusal(fmt.Sprintf("reading deliverable %s v%d", id, version), err)
	}
	var reply wire.ArtifactVersionReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return failure("unreadable deliverable version: %v", err)
	}
	v := reply.Version
	producer, _ := roomlog.VersionProducer(ctx, gatewayFetch(env), room, v)
	out := map[string]any{
		"artifactId": art.ArtifactID, "title": art.Title, "kind": art.Kind, "version": v.Version, "latestVersion": art.CurrentVersion,
		"mimeType": v.MimeType, "sizeBytes": v.SizeBytes, "producedBy": producer.String(),
		"stored": "by bnbloc in this room, not in your workspace",
	}
	if art.Kind != store.ArtifactKindSite && v.SizeBytes > maxArtifactFetchBytes {
		out["note"] = fmt.Sprintf("%d bytes, larger than read_artifact reads (%d): ask the person to open it in the room's Docs panel", v.SizeBytes, maxArtifactFetchBytes)
		return Result{Value: out}
	}
	data, rerr := artifactBytes(ctx, env, base, art, v, in.Path, out)
	if rerr != nil {
		return *rerr
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		out["note"] = "binary content, not shown"
		return Result{Value: out}
	}
	text := string(data)
	if len(text) > maxArtifactText {
		text = roomlog.Truncate(text, maxArtifactText)
		out["truncated"] = true
	}
	out["content"] = roomlog.Fence(producer.String(), text)
	return Result{Value: out}
}

// artifactBytes reads a version's bytes: inline text as stored, a blob
// through the content route, a site's file through the gateway's site file
// read (…/versions/{v}/files/{path}?job=, on the main origin with the
// bridge's session, as the job's requester). It never opens a site ticket:
// a site URL may be on the sites origin, which never sees a session
// (TestRoomToolsNeverOpenSiteTickets). It records the site file read in
// out.
func artifactBytes(ctx context.Context, env *Env, base string, art wire.Artifact, v wire.ArtifactVersion, path string, out map[string]any) ([]byte, *Result) {
	versionPath := base + "/versions/" + strconv.Itoa(v.Version)
	switch {
	case art.Kind == store.ArtifactKindSite:
		p := strings.TrimPrefix(strings.TrimSpace(path), "/")
		if p == "" {
			p = "index.html"
		}
		if clean, ok := core.CleanRelPath(p); !ok || clean != p {
			r := toolError("path %q is not a plain file path of the site", path)
			return nil, &r
		}
		segs := strings.Split(p, "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		job := env.Session.current().JobID
		data, err := env.Gateway.Do(ctx, http.MethodGet, versionPath+"/files/"+strings.Join(segs, "/")+"?job="+url.QueryEscape(job), nil)
		if err != nil {
			r := refusal("reading "+p+" of site "+art.ArtifactID, err)
			return nil, &r
		}
		out["path"] = p
		return data, nil
	case strings.TrimSpace(path) != "":
		r := toolError("path applies only to a site deliverable; %s is a %s", art.ArtifactID, art.Kind)
		return nil, &r
	case v.Storage == store.ArtifactStorageBlob:
		data, err := env.Gateway.Do(ctx, http.MethodGet, versionPath+"/content", nil)
		if err != nil {
			r := refusal("reading deliverable "+art.ArtifactID, err)
			return nil, &r
		}
		return data, nil
	}
	return []byte(v.Content), nil
}

// attachmentIDPrefix starts every room attachment id.
const attachmentIDPrefix = "att_"

// readAttachment reads room attachment id: its meta, then (when not larger
// than read_artifact reads) its bytes, fenced as data a person uploaded.
func readAttachment(ctx context.Context, env *Env, room, id string) Result {
	base := "/v1/rooms/" + url.PathEscape(room) + "/attachments/" + url.PathEscape(id)
	raw, err := env.Gateway.Do(ctx, http.MethodGet, base+"/meta", nil)
	if err != nil {
		return refusal("reading attachment "+id, err)
	}
	var meta wire.ChatAttachment
	if err := json.Unmarshal(raw, &meta); err != nil {
		return failure("unreadable attachment %s: %v", id, err)
	}
	out := map[string]any{
		"attachmentId": meta.ID, "filename": meta.Filename, "mimeType": meta.ContentType, "sizeBytes": meta.Size,
		"stored": "by bnbloc in this room, not in your workspace",
	}
	if meta.Size > maxArtifactFetchBytes {
		out["note"] = fmt.Sprintf("%d bytes, larger than read_artifact reads (%d): ask the person to open it in the room", meta.Size, maxArtifactFetchBytes)
		return Result{Value: out}
	}
	data, err := env.Gateway.Do(ctx, http.MethodGet, base, nil)
	if err != nil {
		return refusal("reading attachment "+id, err)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		out["note"] = "binary content, not shown"
		return Result{Value: out}
	}
	text := string(data)
	if len(text) > maxArtifactText {
		text = roomlog.Truncate(text, maxArtifactText)
		out["truncated"] = true
	}
	out["content"] = roomlog.Fence("a person of the room (attachment "+meta.Filename+")", text)
	return Result{Value: out}
}
