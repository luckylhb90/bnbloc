package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/assist"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// AI assist (docs/prd/2026-10-13-ai-assist.md): the deployment's assist
// model behind every manual entry point, through one API. internal/assist
// owns the model, the prompt and the reply; this file admits the caller,
// resolves the context a task declares as the member reads it, and relays
// the reply.
//
//	GET  /v1/assist                       whether assists run for the caller, and the tasks
//	POST /v1/assist/{task}                one run: SSE for a text task, JSON for a JSON task (gated: its gate first)
//	GET  /v1/platform/assist-model        the platform's model (never its key)
//	PUT  /v1/platform/assist-model        set it and the hosts organizations may use (super admin)
//	POST /v1/platform/assist-model/test   one short completion
//	GET  /v1/tenant/assist-model          the organization's own model (org admin)
//	PUT  /v1/tenant/assist-model          set it, on a host the platform allows
//	POST /v1/tenant/assist-model/test     one short completion through it

// assistRate bounds one member's assist runs: 20 a minute, on top of the
// cost budget every run spends.
const (
	assistRate  = 20.0 / 60.0
	assistBurst = 20.0
)

// maxAssistRequestBytes bounds a run's body (the text plus ids).
const maxAssistRequestBytes = 64 << 10

// errAssistUnavailable: no model, or the organization turned assists off.
var errAssistUnavailable = errors.New("AI assists are not available: the deployment has no assist model, or your organization turned them off")

func (s *Service) assistSettings() *assist.Settings {
	return &assist.Settings{Store: s.PlatformSettings, Box: s.SecretBox, Now: func() int64 { return s.nowTime().UnixMilli() }}
}

// assistModel is the model assists run on for claims' organization: its
// own, else the platform's (assist.Settings.Effective). ok false: the
// organization turned assists off, or no model is configured.
func (s *Service) assistModel(ctx context.Context, claims auth.Claims) (assist.Model, bool, error) {
	if s.PlatformSettings == nil {
		return assist.Model{}, false, nil
	}
	t, err := s.tenantSettings(ctx, claims.Tenant)
	if err != nil && !errors.Is(err, store.ErrTenantNotFound) {
		return assist.Model{}, false, err
	}
	if t.AssistDisabled {
		return assist.Model{}, false, nil
	}
	m, err := s.assistSettings().Effective(ctx, claims.Tenant)
	if err != nil || !m.Configured() {
		return assist.Model{}, false, err
	}
	return m, true, nil
}

// assistTask is a resolved task: its component, version and package.
type assistTask struct {
	Component store.ContentComponent
	Version   store.ContentComponentVersion
	Package   *contentpkg.Package
}

func (s *Service) loadAssistTask(ctx context.Context, c store.ContentComponent) (assistTask, error) {
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, c.LatestVersion)
	if err != nil {
		return assistTask{}, err
	}
	pkg, err := s.contentPackage(ctx, v)
	if err != nil {
		return assistTask{}, err
	}
	return assistTask{Component: c, Version: v, Package: pkg}, nil
}

// handleAssistStatus answers GET /v1/assist: whether assists run for the
// caller and, when they do, the tasks (nearest first by name).
func (s *Service) handleAssistStatus(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	out := wire.AssistStatus{Tasks: []wire.AssistTask{}}
	_, ok, err := s.assistModel(ctx, claims)
	if err != nil {
		obs.ServerError(w, "read the assist model failed", err)
		httpx.Error(w, "read the assist model failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	list, err := assist.Tasks(ctx, s.ContentComponents, viewerOf(claims))
	if err != nil {
		contentStoreErr(w, "list assists failed", err)
		return
	}
	for _, c := range list {
		v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, c.LatestVersion)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			contentStoreErr(w, "load assist failed", err)
			return
		}
		m, err := contentuse.Manifest(v)
		if err != nil {
			continue
		}
		out.Tasks = append(out.Tasks, wire.AssistTask{Name: c.Name, DisplayName: m.DisplayName, ComponentID: c.ID,
			Output: m.Output, Fields: m.Fields, Context: m.Context})
	}
	out.Available = true
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleAssist answers POST /v1/assist/{task}: one run of the nearest task
// of that name on the assist model, with the context the task declares.
func (s *Service) handleAssist(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := r.Context()
	if !s.assistLimiter.Get(assistRate, assistBurst, s.Peers.Size).Allow(claims.Tenant + "/" + claims.Subject) {
		httpx.Error(w, "too many AI assists: try again in a minute", http.StatusTooManyRequests)
		return
	}
	raw, ok := readBody(w, r, maxAssistRequestBytes)
	if !ok {
		return
	}
	var req wire.AssistRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if !utf8.ValidString(req.Text) || utf8.RuneCountInString(req.Text) > wire.MaxAssistText {
		httpx.Error(w, fmt.Sprintf("text must be at most %d characters", wire.MaxAssistText), http.StatusBadRequest)
		return
	}
	if len(req.AttachmentIDs) > wire.MaxAssistAttachments {
		httpx.Error(w, fmt.Sprintf("at most %d attachments", wire.MaxAssistAttachments), http.StatusBadRequest)
		return
	}
	model, ok, err := s.assistModel(ctx, claims)
	if err != nil {
		obs.ServerError(w, "read the assist model failed", err)
		httpx.Error(w, "read the assist model failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, errAssistUnavailable.Error(), http.StatusConflict)
		return
	}
	c, found, err := assist.Task(ctx, s.ContentComponents, viewerOf(claims), r.PathValue("task"))
	if err != nil {
		contentStoreErr(w, "resolve the assist failed", err)
		return
	}
	if !found {
		httpx.Error(w, "no such AI assist", http.StatusNotFound)
		return
	}
	task, err := s.loadAssistTask(ctx, c)
	if err != nil {
		contentStoreErr(w, "load the assist failed", err)
		return
	}
	if req.Gated && task.Package.Manifest.Gate == nil {
		httpx.Error(w, "this AI assist has no gate: run it without \"gated\"", http.StatusBadRequest)
		return
	}
	actx, err := s.assistContext(ctx, claims, task.Package.Manifest.Context, req)
	if err != nil {
		writeAssistContextErr(w, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" && actx.Room == nil && actx.Conversation == nil && actx.Artifact == nil && len(actx.Attachments) == 0 {
		httpx.Error(w, "nothing to work from: type something first", http.StatusBadRequest)
		return
	}
	msgs := assist.Messages(task.Package.Instructions, req.Text, actx)
	if req.Gated && !s.assistGate(w, r, claims, task, msgs) {
		return
	}
	if task.Package.Manifest.Output == wire.AssistOutputJSON {
		s.assistJSON(w, r, claims, task, model, msgs)
		return
	}
	s.assistText(w, r, claims, task, model, msgs)
}

// assistJSON runs a JSON task and answers its fields.
func (s *Service) assistJSON(w http.ResponseWriter, r *http.Request, claims auth.Claims, task assistTask, model assist.Model, msgs []assist.Message) {
	fields, usage, err := s.completeJSONTask(r.Context(), claims, task, model, msgs)
	if err != nil {
		writeAssistModelErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.AssistJSONReply{Fields: fields, Usage: usage})
}

// completeJSONTask is one run of a JSON task: the model's reply read as
// the task's fields, audited (auditAssist). The assist route's runs and
// the gateway's own (runConsentJSON) go through it.
func (s *Service) completeJSONTask(ctx context.Context, claims auth.Claims, task assistTask, model assist.Model, msgs []assist.Message) (map[string]any, wire.AssistUsage, error) {
	text, usage, err := s.assistClient().Complete(ctx, model, msgs, true)
	var fields map[string]any
	if err == nil {
		fields, err = assist.Fields(text, task.Package.Manifest.Fields)
	}
	s.auditAssist(ctx, claims, task, model, usage, err)
	return fields, usage, err
}

// runConsentJSON runs the consent task name (wire.AssistContract.Consent:
// explain-source-use) over text for claims (their organization's model,
// their assist rate and budget), with no context: a run the gateway makes
// to explain an admin's decision, resolved like every run through
// assist.Task, whose consent rule takes the organization's version, else
// the platform's, never a member's own copy. errAssistUnavailable when
// assists are off or no such task is installed.
func (s *Service) runConsentJSON(ctx context.Context, claims auth.Claims, name, text string) (map[string]any, error) {
	if ct, ok := wire.AssistContractOf(name); !ok || !ct.Consent || ct.Output != wire.AssistOutputJSON {
		return nil, fmt.Errorf("%q is not a JSON consent task", name)
	}
	model, ok, err := s.assistModel(ctx, claims)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errAssistUnavailable
	}
	c, found, err := assist.Task(ctx, s.ContentComponents, viewerOf(claims), name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: no %s assist is installed", errAssistUnavailable, name)
	}
	task, err := s.loadAssistTask(ctx, c)
	if err != nil {
		return nil, err
	}
	if !s.assistLimiter.Get(assistRate, assistBurst, s.Peers.Size).Allow(claims.Tenant + "/" + claims.Subject) {
		return nil, errors.New("too many AI assists: try again in a minute")
	}
	fields, _, err := s.completeJSONTask(ctx, claims, task, model, assist.Messages(task.Package.Instructions, text, assist.Context{}))
	return fields, err
}

// assistText runs a text task as Server-Sent Events: "delta" {text} per
// piece, then "done" {usage}, or "error" {error} once the stream started.
// A failure before the first piece answers an HTTP error instead.
func (s *Service) assistText(w http.ResponseWriter, r *http.Request, claims auth.Claims, task assistTask, model assist.Model, msgs []assist.Message) {
	rc := http.NewResponseController(w)
	started := false
	start := func() {
		if started {
			return
		}
		started = true
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}
	send := func(event string, v any) error {
		start()
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		return rc.Flush()
	}
	usage, err := s.assistClient().Stream(r.Context(), model, msgs, func(piece string) error {
		return send("delta", wire.AssistDelta{Text: piece})
	})
	s.auditAssist(r.Context(), claims, task, model, usage, err)
	switch {
	case err == nil:
		_ = send("done", wire.AssistDone{Usage: usage})
	case started:
		_ = send("error", map[string]string{"error": assistErrText(err)})
	default:
		writeAssistModelErr(w, err)
	}
}

// assistClient is the model client (the tests' transport when set).
func (s *Service) assistClient() *assist.Client {
	return &assist.Client{Transport: s.AssistTransport, Now: s.nowTime}
}

// auditAssist records a task's run (auditModelRun).
func (s *Service) auditAssist(ctx context.Context, claims auth.Claims, task assistTask, model assist.Model, usage wire.AssistUsage, runErr error) {
	s.auditModelRun(ctx, claims, task.Component.Name+"@"+task.Version.Version, "component "+task.Component.ID, model, usage, runErr)
}

// auditModelRun records one assist model call: what ran (name, and what
// it ran for), the model (and whether it is the organization's own), the
// tokens and the latency, never the text. It is the audit of every call
// of the assist model (a task, an app's ai).
func (s *Service) auditModelRun(ctx context.Context, claims auth.Claims, name, what string, model assist.Model, usage wire.AssistUsage, runErr error) {
	modelName := firstNonEmpty(usage.Model, model.Model)
	if model.Org {
		modelName += " (the organization's)"
	}
	outcome, detail := wire.AuditOutcomeAllow, fmt.Sprintf("%s, model %s, %d prompt + %d completion tokens, %d ms",
		what, modelName, usage.PromptTokens, usage.CompletionTokens, usage.LatencyMs)
	if runErr != nil {
		outcome, detail = "failed", detail+", failed: "+assistErrClass(runErr)
	}
	s.audit(ctx, claims.Tenant, store.ExtAuditRecord{
		Kind: wire.AuditKindAssist, Name: name, Action: "run",
		Actor: claims.Subject, Outcome: outcome, Detail: detail,
	})
}

// assistErrClass names a failure for the audit row without the provider's
// message (it may quote the request).
func assistErrClass(err error) string {
	var me *assist.ModelError
	switch {
	case errors.As(err, &me):
		return fmt.Sprintf("model answered %d", me.Status)
	case errors.Is(err, assist.ErrBadReply):
		return "unreadable reply"
	case errors.Is(err, context.Canceled):
		return "caller went away"
	}
	return "model unreachable"
}

func assistErrText(err error) string {
	var me *assist.ModelError
	if errors.As(err, &me) || errors.Is(err, assist.ErrBadReply) {
		return err.Error()
	}
	return "the assist model could not be reached: " + err.Error()
}

func writeAssistModelErr(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	obs.ServerError(w, "the assist model failed", err)
	httpx.Error(w, assistErrText(err), http.StatusBadGateway)
}

// errAssistNotFound: a room, deliverable, expert or attachment the request
// names is not one the member reads.
var errAssistNotFound = errors.New("not found")

func writeAssistContextErr(w http.ResponseWriter, err error) {
	var bad assistBadRequest
	switch {
	case errors.As(err, &bad):
		httpx.Error(w, string(bad), http.StatusBadRequest)
	case errors.Is(err, errAssistNotFound):
		httpx.Error(w, "the room, deliverable, expert or attachment is not one you can read", http.StatusNotFound)
	default:
		obs.ServerError(w, "read the assist's context failed", err)
		httpx.Error(w, "read the assist's context failed", http.StatusBadGateway)
	}
}

type assistBadRequest string

func (e assistBadRequest) Error() string { return string(e) }

// assistContext resolves the contexts the task declares (others are never
// read, whatever the request names) as the member reads them: the room
// (its name, expert, skill packs and recent messages, and the turn the
// request names), a deliverable of it (title, type, file, the request that
// produced it), attachments of it (names and types), the library (the
// experts and skill packs the member may use), the clock (now in the
// request's timezone) and the capabilities (what the member could use).
func (s *Service) assistContext(ctx context.Context, claims auth.Claims, declared []string, req wire.AssistRequest) (assist.Context, error) {
	var out assist.Context
	var err error
	roomID := strings.TrimSpace(req.RoomID)
	wants := func(c string) bool { return slices.Contains(declared, c) }
	if wants(wire.AssistContextClock) {
		out.Clock = assist.ClockIn(s.nowTime(), req.Timezone)
	}
	if wants(wire.AssistContextLibrary) && s.ContentComponents != nil {
		lib, err := assist.LibraryOf(ctx, s.ContentComponents, viewerOf(claims))
		if err != nil {
			return out, err
		}
		out.Library = lib
	}
	if wants(wire.AssistContextCapabilities) && roomID == "" {
		if out.Capabilities, err = s.assistCapabilities(ctx, claims, auth.Claims{}, "", req.HasOwnAgent); err != nil {
			return out, err
		}
	}
	needsRoom := (wants(wire.AssistContextRoom) && roomID != "") ||
		(wants(wire.AssistContextCapabilities) && roomID != "") ||
		(wants(wire.AssistContextConversation) && roomID != "") ||
		(wants(wire.AssistContextArtifact) && req.ArtifactID != "") ||
		(wants(wire.AssistContextAttachments) && len(req.AttachmentIDs) > 0)
	if !needsRoom {
		return out, nil
	}
	if roomID == "" {
		return out, assistBadRequest("a deliverable and attachments are a room's: name the room (roomId)")
	}
	rc, ok := s.roomClaims(claims, roomID)
	if !ok {
		return out, errAssistNotFound
	}
	if wants(wire.AssistContextRoom) {
		channelID := strings.TrimSpace(req.ChannelID)
		room, err := s.assistRoom(ctx, rc, roomID, channelID, strings.TrimSpace(req.ExpertID))
		if err != nil {
			return out, err
		}
		if req.ReplySeq > 0 {
			if room.Turn, err = s.assistTurn(ctx, rc, roomID, channelID, req.ReplySeq); err != nil {
				return out, err
			}
		}
		out.Room = room
	}
	if wants(wire.AssistContextCapabilities) {
		if out.Capabilities, err = s.assistCapabilities(ctx, claims, rc, roomID, req.HasOwnAgent); err != nil {
			return out, err
		}
	}
	if wants(wire.AssistContextConversation) {
		if out.Conversation, err = s.assistConversation(ctx, rc, roomID); err != nil {
			return out, err
		}
	}
	if wants(wire.AssistContextArtifact) && req.ArtifactID != "" {
		art, err := s.assistArtifact(ctx, rc, roomID, strings.TrimSpace(req.ArtifactID))
		if err != nil {
			return out, err
		}
		out.Artifact = art
	}
	if wants(wire.AssistContextAttachments) {
		for _, id := range req.AttachmentIDs {
			meta, found, err := s.attachmentMeta(ctx, rc, roomID, strings.TrimSpace(id))
			if err != nil {
				return out, err
			}
			if !found {
				return out, errAssistNotFound
			}
			out.Attachments = append(out.Attachments, assist.Attachment{Name: meta.Filename, ContentType: meta.ContentType})
		}
	}
	return out, nil
}

// assistRoom reads a room as its member: the room service answers its
// name to members only, realtime its recent messages (in the channel, the
// default one when ""), and the Library its expert (the one picked for the
// message, else the room's) and skill packs (the room's and the member's).
func (s *Service) assistRoom(ctx context.Context, claims auth.Claims, roomID, channelID, expertID string) (*assist.Room, error) {
	name, err := s.assistRoomName(ctx, claims, roomID)
	if err != nil {
		return nil, err
	}
	room := &assist.Room{Name: name}
	if room.Recent, err = s.assistRecent(ctx, claims, roomID, channelID); err != nil {
		return nil, err
	}
	if s.ContentComponents == nil {
		return room, nil
	}
	if expertID == "" {
		if expertID, err = s.roomExpertOf(ctx, roomID); err != nil {
			return nil, err
		}
	}
	if expertID != "" {
		c, err := s.ContentComponents.GetContentComponent(ctx, expertID)
		switch {
		case errors.Is(err, store.ErrContentNotFound):
		case err != nil:
			return nil, err
		case c.Kind == contentpkg.KindExpert && (extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Installed || s.contentInRoom(ctx, claims, roomID, c.ID)):
			room.Expert = &assist.Part{Name: firstNonEmpty(c.DisplayName, c.Name), Description: c.Description}
		}
	}
	roomUses, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseRoom, roomID)
	if err != nil {
		return nil, err
	}
	memberUses, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseMember, claims.Subject)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i, u := range append(roomUses, memberUses...) {
		if u.Kind != contentpkg.KindSkill || seen[u.ComponentID] {
			continue
		}
		c, err := s.ContentComponents.GetContentComponent(ctx, u.ComponentID)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if i >= len(roomUses) && !extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Installed {
			continue
		}
		seen[c.ID] = true
		room.Skills = append(room.Skills, assist.Part{Name: firstNonEmpty(c.DisplayName, c.Name), Description: c.Description})
	}
	return room, nil
}

// assistRoomName reads a room's name as its member: the room service
// answers it to members only (errAssistNotFound for anyone else, and for a
// preview).
func (s *Service) assistRoomName(ctx context.Context, claims auth.Claims, roomID string) (string, error) {
	var detail struct {
		Name    string `json:"name"`
		Preview bool   `json:"preview"`
	}
	err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID), "", nil, claims.Subject, claims.Tenant, &detail)
	var rbe *roomBackendError
	switch {
	case errors.As(err, &rbe) && rbe.Status < http.StatusInternalServerError:
		return "", errAssistNotFound
	case err != nil:
		return "", err
	case detail.Preview:
		return "", errAssistNotFound
	}
	return detail.Name, nil
}

// assistRecent reads a room channel's latest messages as its member
// (realtime decides who reads it), oldest first, through the one history
// row decoder.
func (s *Service) assistRecent(ctx context.Context, claims auth.Claims, roomID, channelID string) ([]assist.Line, error) {
	rows, err := s.roomHistory(ctx, memberReader(claims), roomID, channelID, 0, 30)
	if err != nil {
		return nil, err
	}
	var lines []assist.Line
	for _, row := range rows {
		for _, e := range roomlog.DecodeRow(row) {
			if strings.TrimSpace(e.Text) == "" {
				continue // a message that carried only files
			}
			lines = append(lines, assist.Line{Speaker: e.Speaker, Text: e.Text})
		}
	}
	if len(lines) > assist.RecentMessages {
		lines = lines[len(lines)-assist.RecentMessages:]
	}
	return lines, nil
}

// historyReader is who reads a room's history through roomHistory: a
// member (memberReader: realtime decides what they read), or the gateway
// itself for a consented read (read_records alone, once the grant's cover
// decided the room: realtime's internal route, never the people chat).
type historyReader struct {
	subject, tenant string
	consented       bool
}

// memberReader is claims' member reading a room's history.
func memberReader(claims auth.Claims) historyReader {
	return historyReader{subject: claims.Subject, tenant: claims.Tenant}
}

// roomHistory is the gateway's one read of a room channel's history
// (realtime's): the newest limit rows below the sequence before (0: from
// the newest), oldest first. A room the reader does not read is
// errAssistNotFound.
func (s *Service) roomHistory(ctx context.Context, h historyReader, roomID, channelID string, before int64, limit int) ([]roomlog.Row, error) {
	base := strings.TrimRight(s.Backends.Realtime, "/")
	if base == "" {
		return nil, nil
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if channelID != "" {
		q.Set("channelId", channelID)
	}
	if before > 0 {
		q.Set("before", strconv.FormatInt(before, 10))
	}
	route := "/v1/rooms/"
	if h.consented {
		route = "/internal/v1/rooms/"
	}
	resp, err := callBackend(ctx, http.MethodGet, base+route+url.PathEscape(roomID)+"/history?"+q.Encode(), nil, h.subject, h.tenant)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		return nil, upstreamStatusError(resp)
	case resp.StatusCode != http.StatusOK:
		return nil, errAssistNotFound
	}
	var page struct {
		Messages []roomlog.Row `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode room history: %w", err)
	}
	return page.Messages, nil
}

// assistArtifact reads a deliverable of the room as its member: its
// title, type and file, and the request that produced its current version
// (the one recipe owner's capture).
func (s *Service) assistArtifact(ctx context.Context, claims auth.Claims, roomID, artifactID string) (*assist.Artifact, error) {
	art, v, err := s.deliverableVersion(ctx, claims, roomID, artifactID)
	if errors.Is(err, errSiteNotFound) {
		return nil, errAssistNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &assist.Artifact{Title: art.Title, FileName: art.FileName,
		Type: contentpkg.WorkTypeOf(contentpkg.WorkDeliverable{Artifact: art, FileName: art.FileName})}
	captured, err := s.deliverableRecipe(ctx, claims, roomID, v)
	if err != nil {
		return nil, err
	}
	if captured.Recipe != nil {
		out.Request = captured.Recipe.Prompt
	}
	return out, nil
}

// handleAssistModel answers GET /v1/platform/assist-model.
func (s *Service) handleAssistModel(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	view, err := s.assistSettings().View(r.Context())
	if err != nil {
		obs.ServerError(w, "read the assist model failed", err)
		httpx.Error(w, "read the assist model failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// handlePutAssistModel answers PUT /v1/platform/assist-model.
func (s *Service) handlePutAssistModel(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if view, ok := s.putAssistModel(w, r, claims, s.assistSettings()); ok {
		httpx.WriteJSON(w, http.StatusOK, view)
	}
}

// putAssistModel stores a model update (the platform's or an
// organization's) and audits it; a refusal is answered (ok false).
func (s *Service) putAssistModel(w http.ResponseWriter, r *http.Request, claims auth.Claims, settings *assist.Settings) (wire.AssistModelConfig, bool) {
	var u wire.AssistModelUpdate
	if !httpx.DecodeJSON(w, r, 16<<10, &u) {
		return wire.AssistModelConfig{}, false
	}
	view, err := settings.Put(r.Context(), u, claims.Subject)
	switch {
	case errors.Is(err, assist.ErrSettingsOff):
		httpx.Error(w, err.Error(), http.StatusConflict)
		return wire.AssistModelConfig{}, false
	case errors.Is(err, assist.ErrInvalidSettings):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return wire.AssistModelConfig{}, false
	case err != nil:
		obs.ServerError(w, "save the assist model failed", err)
		httpx.Error(w, "save the assist model failed", http.StatusInternalServerError)
		return wire.AssistModelConfig{}, false
	}
	name, action, detail := "assist-model", "model-set", "model "+view.Model+" at "+hostOf(view.BaseURL)
	if len(view.OrgHosts) > 0 {
		detail += ", organizations may use " + strings.Join(view.OrgHosts, ", ")
	}
	if settings.Tenant != "" {
		name = "org-assist-model"
	}
	if view.BaseURL == "" {
		action, detail = "model-clear", "assists turned off"
		if settings.Tenant != "" {
			detail = "back to the platform's model"
		}
	}
	s.audit(r.Context(), claims.Tenant, store.ExtAuditRecord{Kind: wire.AuditKindAssist, Name: name, Action: action,
		Actor: claims.Subject, Outcome: wire.AuditOutcomeAllow, Detail: detail})
	return view, true
}

// orgAssistModel is the organization's own model view: the model, the
// hosts it may name and the platform's model name.
func (s *Service) orgAssistModel(ctx context.Context, tenant string) (wire.AssistOrgModel, error) {
	settings := s.assistSettings()
	own, err := settings.Org(tenant).View(ctx)
	if err != nil {
		return wire.AssistOrgModel{}, err
	}
	out := wire.AssistOrgModel{Model: own, AllowedHosts: []string{}}
	platform, err := settings.View(ctx)
	if err != nil {
		return wire.AssistOrgModel{}, err
	}
	if platform.BaseURL != "" {
		out.PlatformModel = platform.Model
	}
	if platform.OrgHosts != nil {
		out.AllowedHosts = platform.OrgHosts
	}
	return out, nil
}

// handleOrgAssistModel answers GET /v1/tenant/assist-model.
func (s *Service) handleOrgAssistModel(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	out, err := s.orgAssistModel(r.Context(), claims.Tenant)
	if err != nil {
		obs.ServerError(w, "read the assist model failed", err)
		httpx.Error(w, "read the assist model failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handlePutOrgAssistModel answers PUT /v1/tenant/assist-model: the
// organization's own model, on a host the platform allows.
func (s *Service) handlePutOrgAssistModel(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if _, ok := s.putAssistModel(w, r, claims, s.assistSettings().Org(claims.Tenant)); !ok {
		return
	}
	s.handleOrgAssistModel(w, r, claims)
}

// handleTestOrgAssistModel answers POST /v1/tenant/assist-model/test.
func (s *Service) handleTestOrgAssistModel(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	s.testAssistModel(w, r, s.assistSettings().Org(claims.Tenant))
}

// handleTestAssistModel answers POST /v1/platform/assist-model/test.
func (s *Service) handleTestAssistModel(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	s.testAssistModel(w, r, s.assistSettings())
}

// testAssistModel sends one short completion through a stored model; a
// failure is the answer, not an HTTP error.
func (s *Service) testAssistModel(w http.ResponseWriter, r *http.Request, settings *assist.Settings) {
	m, err := settings.Model(r.Context())
	if err != nil {
		obs.ServerError(w, "read the assist model failed", err)
		httpx.Error(w, "read the assist model failed", http.StatusInternalServerError)
		return
	}
	if !m.Configured() {
		httpx.Error(w, "no assist model is configured", http.StatusConflict)
		return
	}
	reply, usage, err := s.assistClient().Complete(r.Context(), m, []assist.Message{
		{Role: "system", Content: "Reply with the single word OK."},
		{Role: "user", Content: "ping"},
	}, false)
	out := wire.AssistModelTest{OK: err == nil, Usage: usage}
	if err != nil {
		out.Error = assistErrText(err)
	} else if out.Reply = strings.TrimSpace(reply); utf8.RuneCountInString(out.Reply) > 200 {
		out.Reply = string([]rune(out.Reply)[:200]) + "…"
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}
