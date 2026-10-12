package realtimehttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Agent dispatch (room tools, docs/prd/2026-10-12-experts-and-teams.md §7).
//
// POST /internal/v1/rooms/{roomId}/dispatch is how an agent asks another
// agent of the room for part of its work. The caller is a bridge acting as
// its owner (the gateway signs that identity; its public door is POST
// /v1/rooms/{roomId}/dispatch). RouteKey sends the request to the room's
// owner replica, which posts the message: the hub is touched only there.
//
// The dispatch carries no authority of its own. The message is posted
// through PostChat AS THE PARENT JOB'S HUMAN REQUESTER, so the new job is
// that person's request in every respect — the target bridge runs borrow
// authorization, reservation, quota, budgets and sandbox policy exactly as
// for a typed message. The guards below only bound what an agent can do
// with the requester's authority while it holds one of their jobs.

// DispatchStore is the job read side the dispatch guards need: the parent
// chain (GetAgentRun), a parent's dispatches and a bridge's owner.
type DispatchStore interface {
	store.AgentRunReader
	store.AgentDispatchReader
}

// AgentDispatchAuditKind is the extension_audit kind of dispatch decisions.
const AgentDispatchAuditKind = "agent_dispatch"

// dispatchClientMsgIDShape bounds a caller's idempotency id (it is
// namespaced under the parent job before it reaches the room feed).
var dispatchClientMsgIDShape = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// dispatchRefusal is a guard's refusal: an HTTP status and a wire code.
type dispatchRefusal struct {
	status int
	code   string
	msg    string
}

func (e *dispatchRefusal) Error() string { return e.msg }

func refuse(status int, code, format string, args ...any) error {
	return &dispatchRefusal{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

// dispatchFailure is a dispatch that failed on a read of its own (the
// parent chain, a bridge owner, the dispatch listing): a 500 that names the
// failing step, as opposed to a PostChat error.
type dispatchFailure struct{ err error }

func (e dispatchFailure) Error() string { return e.err.Error() }
func (e dispatchFailure) Unwrap() error { return e.err }

func failed(format string, err error) error { return dispatchFailure{fmt.Errorf(format, err)} }

// dispatchStripes serializes the fan-out count and the post of dispatches
// that share a parent job (striped by parent id, so no per-key state is
// kept). Every dispatch of a parent job lands on the room owner replica
// (the parent lives in the dispatch's room), so one process sees them all.
var dispatchStripes [64]sync.Mutex

func dispatchStripe(parentJobID string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(parentJobID))
	return &dispatchStripes[h.Sum32()%uint32(len(dispatchStripes))]
}

// handleInternalDispatch serves POST /internal/v1/rooms/{roomId}/dispatch.
func (s *Service) handleInternalDispatch(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	caller := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if caller == "" {
		http.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	if s.Dispatches == nil || s.BridgeJobs == nil {
		obs.ServerError(w, "agent dispatch not configured", nil)
		http.Error(w, "agent dispatch not configured", http.StatusServiceUnavailable)
		return
	}
	var in wire.DispatchRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxChatBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": wire.DispatchInvalid, "error": "invalid json body"})
		return
	}
	reply, err := s.Dispatch(r.Context(), roomID, caller, in)
	var refusal *dispatchRefusal
	var failure dispatchFailure
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, reply)
	case errors.As(err, &refusal):
		writeJSON(w, refusal.status, map[string]string{"code": refusal.code, "error": refusal.msg})
	case errors.As(err, &failure):
		obs.ServerError(w, "agent dispatch failed", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "agent dispatch failed"})
	default:
		writeChatPostErr(w, err)
	}
}

// Dispatch checks the guards and posts one dispatch as the parent job's
// requester. A refusal is a *dispatchRefusal; any other error is a PostChat
// error (writeChatPostErr maps it). Every decision is audited.
func (s *Service) Dispatch(ctx context.Context, roomID, caller string, in wire.DispatchRequest) (reply wire.DispatchReply, err error) {
	var parent store.AgentJob
	defer func() { s.auditDispatch(ctx, caller, parent, in, reply, err) }()
	in.ParentJobID = strings.TrimSpace(in.ParentJobID)
	in.Recipient.OwnerID = strings.TrimSpace(in.Recipient.OwnerID)
	in.Recipient.Provider = strings.TrimSpace(in.Recipient.Provider)
	in.Recipient.Core = strings.TrimSpace(in.Recipient.Core)
	switch {
	case in.ParentJobID == "":
		return reply, refuse(http.StatusBadRequest, wire.DispatchInvalid, "parentJobId is required")
	case in.Recipient.OwnerID == "" || in.Recipient.Provider == "":
		return reply, refuse(http.StatusBadRequest, wire.DispatchInvalid, "recipient ownerId and provider are required")
	case in.ClientMsgID != "" && !dispatchClientMsgIDShape.MatchString(in.ClientMsgID):
		return reply, refuse(http.StatusBadRequest, wire.DispatchInvalid, "clientMsgId must be 1-64 letters, digits, '-' or '_'")
	case in.Role != "" && !contentpkg.ValidName(in.Role):
		return reply, refuse(http.StatusBadRequest, wire.DispatchInvalid, "role must be lower-case letters, digits and hyphens")
	}

	parent, err = s.Dispatches.GetAgentRun(ctx, in.ParentJobID)
	if errors.Is(err, store.ErrAgentJobNotFound) || (err == nil && parent.RoomID != roomID) {
		parent = store.AgentJob{}
		return reply, refuse(http.StatusNotFound, wire.DispatchParentNotFound, "parent job not found in this room")
	}
	if err != nil {
		return reply, failed("read parent job: %w", err)
	}
	if err := s.checkDispatchAuthority(ctx, roomID, caller, parent); err != nil {
		return reply, err
	}
	depth := parent.DispatchDepth + 1
	if depth > wire.MaxDispatchDepth {
		return reply, refuse(http.StatusConflict, wire.DispatchDepthExceeded, "dispatch depth limit (%d) reached", wire.MaxDispatchDepth)
	}
	if sameAgent(in.Recipient, parent) {
		return reply, refuse(http.StatusConflict, wire.DispatchSelf, "an agent cannot dispatch to itself")
	}
	if err := s.checkDispatchChain(ctx, in.Recipient, parent); err != nil {
		return reply, err
	}

	// The fan-out count and the post are one critical section per parent, so
	// concurrent dispatches of one job cannot both pass the count.
	stripe := dispatchStripe(parent.JobID)
	stripe.Lock()
	defer stripe.Unlock()
	clientMsgID := ""
	if in.ClientMsgID != "" {
		clientMsgID = "dispatch-" + parent.JobID + "-" + in.ClientMsgID
	}
	children, err := s.Dispatches.ListDispatchedJobs(ctx, parent.JobID)
	if err != nil {
		return reply, failed("list dispatched jobs: %w", err)
	}
	for _, c := range children {
		if clientMsgID != "" && c.ClientMsgID == clientMsgID {
			// An idempotent retry of a dispatch that already went out.
			return wire.DispatchReply{Sequence: c.MessageSeq, JobID: c.JobID, Depth: c.DispatchDepth}, nil
		}
	}
	if len(children) >= wire.MaxDispatchFanOut {
		return reply, refuse(http.StatusConflict, wire.DispatchFanOutExceeded, "this job already dispatched %d tasks (the limit)", wire.MaxDispatchFanOut)
	}
	posted, err := s.PostChat(ctx, ChatPost{
		RoomID:      roomID,
		Author:      parent.RequesterUserID,
		Text:        in.Text,
		ClientMsgID: clientMsgID,
		ChannelID:   parent.ChannelID,
		Recipient:   &ChatRecipient{OwnerID: in.Recipient.OwnerID, Provider: in.Recipient.Provider, Core: in.Recipient.Core},
		Dispatched:  &wire.DispatchMarker{JobID: parent.JobID, Depth: depth, Via: agentHandle(parent.OwnerUserID, parent.Provider), Role: in.Role},
		Expert:      in.Expert,
	})
	switch {
	case errors.Is(err, errAgentInPeopleChat):
		return reply, refuse(http.StatusBadRequest, wire.DispatchPeopleChat, "agents cannot be addressed in the people chat")
	case errors.Is(err, errExpertNotAvailable):
		return reply, refuse(http.StatusBadRequest, wire.DispatchExpertUnusable, "the expert does not exist or the requester may not use it here")
	}
	if err != nil {
		return reply, err
	}
	if posted.Job == nil {
		return reply, dispatchFailure{fmt.Errorf("dispatch posted at sequence %d but no agent job was queued", posted.Sequence)}
	}
	return wire.DispatchReply{Sequence: posted.Sequence, JobID: posted.Job.JobID, Depth: depth}, nil
}

// checkDispatchAuthority admits a dispatch only while its parent job is
// being driven by a bridge of the caller, on behalf of a requester who is
// still a person in the room: the caller can only spend authority that is
// running on its own machine right now.
func (s *Service) checkDispatchAuthority(ctx context.Context, roomID, caller string, parent store.AgentJob) error {
	switch err := store.JobLeasedBy(ctx, s.Dispatches, parent, caller); {
	case errors.Is(err, store.ErrJobNotLive):
		return refuse(http.StatusConflict, wire.DispatchParentNotRunning, "parent job is %s and not running on a bridge", parent.Status)
	case errors.Is(err, store.ErrJobNotCallers):
		return refuse(http.StatusForbidden, wire.DispatchNotParentBridge, "the parent job is not running on the caller's bridge")
	case err != nil:
		return failed("read bridge owner: %w", err)
	}
	tid := tenant.FromContext(ctx)
	if s.Membership == nil || !s.Membership.IsMember(roomID, caller, tid) {
		return refuse(http.StatusForbidden, wire.DispatchNotParentBridge, "caller is not a member of room")
	}
	if parent.RequesterUserID == "" || !s.Membership.IsHumanMember(roomID, parent.RequesterUserID, tid) {
		return refuse(http.StatusForbidden, wire.DispatchRequesterGone, "the parent job's requester is no longer a member of the room")
	}
	return nil
}

// checkDispatchChain refuses a dispatch to an agent already working on the
// request higher up the chain (a cycle). The walk is bounded by the depth
// limit; an ancestor that no longer exists (retention) ends the chain.
func (s *Service) checkDispatchChain(ctx context.Context, to wire.DispatchRecipient, parent store.AgentJob) error {
	cur := parent
	for hops := 0; cur.DispatchedByJobID != "" && hops < wire.MaxDispatchDepth; hops++ {
		up, err := s.Dispatches.GetAgentRun(ctx, cur.DispatchedByJobID)
		if errors.Is(err, store.ErrAgentJobNotFound) {
			return nil
		}
		if err != nil {
			return failed("read dispatch chain: %w", err)
		}
		if sameAgent(to, up) {
			return refuse(http.StatusConflict, wire.DispatchCycle, "%s is already working on this request higher up the chain", agentHandle(up.OwnerUserID, up.Provider))
		}
		cur = up
	}
	return nil
}

// sameAgent reports whether a dispatch recipient addresses the agent that
// runs job. An empty core on either side means "the owner's default core",
// so it matches any core of the same owner and provider (refusing a
// possible self-dispatch rather than letting it through).
func sameAgent(to wire.DispatchRecipient, job store.AgentJob) bool {
	if !strings.EqualFold(to.OwnerID, job.OwnerUserID) || !strings.EqualFold(to.Provider, job.Provider) {
		return false
	}
	return to.Core == "" || job.Core == "" || strings.EqualFold(to.Core, job.Core)
}

// agentHandle is an agent's display handle, "owner/provider" (e.g.
// "jack/codex"), the attribution the feed shows for agent replies.
func agentHandle(owner, provider string) string {
	if owner == "" {
		return provider
	}
	return owner + "/" + provider
}

// auditDispatch records one dispatch decision in extension_audit: the actor
// is the bridge owner who dispatched, the name the addressed agent, and the
// detail the parent job, the requester whose authority it ran under and,
// when admitted, the new job and its depth. Best-effort: a failed write is
// logged and never fails the dispatch.
func (s *Service) auditDispatch(ctx context.Context, caller string, parent store.AgentJob, in wire.DispatchRequest, reply wire.DispatchReply, err error) {
	if s.Audit == nil {
		return
	}
	outcome := "allow"
	var refusal *dispatchRefusal
	switch {
	case errors.As(err, &refusal):
		outcome = "deny"
	case err != nil:
		outcome = "failed"
	}
	detail := []string{"parentJob=" + in.ParentJobID}
	if parent.JobID != "" {
		detail = append(detail, "via="+agentHandle(parent.OwnerUserID, parent.Provider), "requester="+parent.RequesterUserID)
	}
	if reply.JobID != "" {
		detail = append(detail, "job="+reply.JobID, fmt.Sprintf("depth=%d", reply.Depth))
	}
	if in.Expert != nil && in.Expert.ID != "" {
		detail = append(detail, "expert="+in.Expert.ID)
	}
	if in.Role != "" {
		detail = append(detail, "role="+in.Role)
	}
	switch {
	case refusal != nil:
		detail = append(detail, "reason="+refusal.code)
	case err != nil:
		detail = append(detail, "error="+err.Error())
	}
	rec := store.ExtAuditRecord{
		Kind:     AgentDispatchAuditKind,
		Name:     agentHandle(in.Recipient.OwnerID, in.Recipient.Provider),
		Action:   "dispatch",
		Actor:    caller,
		TenantID: tenant.FromContext(ctx),
		Outcome:  outcome,
		Detail:   strings.Join(detail, " "),
		AtMs:     time.Now().UnixMilli(),
	}
	if aerr := s.Audit.AppendExtAudit(context.WithoutCancel(ctx), rec); aerr != nil {
		slog.Warn("bnbloc realtime dispatch audit failed", "event", "bnbloc.realtime.dispatch_audit_failed", "error", aerr)
	}
}
