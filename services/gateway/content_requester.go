package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Content requests on behalf of a running job's requester
// (docs/prd/2026-10-12-experts-and-teams.md §5, docs/architecture/
// room-tools.md): the room tools' save_component, get_component and
// list_components call the ordinary content routes from the bridge running
// a job, with a ?job=&room= grant. The authority model is dispatch's: the
// bridge never acts as itself or its owner; a grant is admitted only while
// the job is live and leased by a bridge of the caller (store.JobLeasedBy)
// and its requester may still post in the room (membership.Member.MayChat),
// and the request then acts as that requester: what they may read is read,
// and a save lands in their own Library (personal scope, owner = the
// requester).
//
// contentGrant is the ONE resolution of who a content request acts as; every
// content route (contentCaller) and the bundle download (artifactEntitled)
// go through it. Each route declares its jobGrant mode
// (docs/prd/2026-10-12-code-health-remediation.md O6): the actions a room
// tool performs honor the grant, organization-visible or destructive ones
// refuse ?job= with a 400, so no route can silently act as the bridge owner.
//
// One extra rule applies to saves: when the agent is not the requester's
// own (a borrowed or platform agent), a package that ships scripts is
// refused. Someone else's machine may not plant executable content into a
// member's own Library, where "own content" runs unsandboxed; the member
// can still save it explicitly from the turn's deliverables.

// contentActor is who a content request acts as.
type contentActor struct {
	// Claims is the acting member: the caller, or a job's requester, in the
	// tenant the request acts in (the named room's, tenant_scope.go).
	auth.Claims
	// Room is the room the request names (?room=), checked in reach; "" for
	// none.
	Room string
	// Job is the job of a granted bridge request (nil for a member's own
	// request).
	Job *store.AgentJob
	// Bridge is the subject whose bridge made a granted request.
	Bridge string
}

// jobGrant is how a content route treats a ?job=&room= grant.
type jobGrant int

const (
	// grantHonored: a bridge running the job acts as its requester (the
	// actions room tools perform: list, read, save, import, publish, stage
	// a draft, prompt save/use, content uses).
	grantHonored jobGrant = iota
	// grantRefused: ?job= answers 400 (delete, ack, share, list or revoke
	// shares, update a copy: organization-visible or destructive actions a
	// job never performs).
	grantRefused
)

// grantError is a refused grant: its status and the reason a client reads.
type grantError struct {
	status int
	msg    string
	cause  error // a 5xx's underlying failure
}

func (e *grantError) Error() string { return e.msg }

func refuseGrant(status int, msg string) error { return &grantError{status: status, msg: msg} }

// writeGrantErr answers a contentGrant failure.
func writeGrantErr(w http.ResponseWriter, err error) {
	var ge *grantError
	if !errors.As(err, &ge) {
		ge = &grantError{status: http.StatusInternalServerError, msg: "resolve content grant failed", cause: err}
	}
	httpx.WriteStatus(w, ge.status, ge.msg, ge.cause)
}

// contentGrant resolves who a content request acts as. Without ?job= the
// caller acts as themselves, in the room's tenant when the request names a
// room (?room=) they can reach. With ?job=&room= the bridge running the job
// acts as the job's requester (see above).
func (s *Service) contentGrant(r *http.Request, caller auth.Claims) (contentActor, error) {
	q := r.URL.Query()
	roomID := strings.TrimSpace(q.Get("room"))
	jobID := strings.TrimSpace(q.Get("job"))
	if roomID == "" {
		if jobID != "" {
			return contentActor{}, refuseGrant(http.StatusBadRequest, "job and room required")
		}
		return contentActor{Claims: caller}, nil
	}
	claims, ok := s.roomClaims(caller, roomID)
	if !ok {
		return contentActor{}, refuseGrant(http.StatusForbidden, "room scope not authorized")
	}
	if jobID == "" {
		return contentActor{Claims: claims, Room: roomID}, nil
	}
	if s.ContentJobs == nil {
		return contentActor{}, &grantError{status: http.StatusServiceUnavailable, msg: "agent jobs unavailable"}
	}
	ctx := tenant.NewContext(r.Context(), claims.Tenant)
	job, err := s.ContentJobs.GetAgentRun(ctx, jobID)
	if errors.Is(err, store.ErrAgentJobNotFound) || (err == nil && job.RoomID != roomID) {
		return contentActor{}, refuseGrant(http.StatusNotFound, "job not found")
	}
	if err != nil {
		return contentActor{}, &grantError{status: http.StatusInternalServerError, msg: "load agent job failed", cause: err}
	}
	switch err := store.JobLeasedBy(ctx, s.ContentJobs, job, caller.Subject); {
	case errors.Is(err, store.ErrJobNotLive):
		return contentActor{}, refuseGrant(http.StatusConflict, "job is not running")
	case errors.Is(err, store.ErrJobNotCallers):
		return contentActor{}, refuseGrant(http.StatusForbidden, "job is not running on your bridge")
	case err != nil:
		return contentActor{}, &grantError{status: http.StatusInternalServerError, msg: "load bridge owner failed", cause: err}
	}
	if job.TenantID == "" {
		job.TenantID = claims.Tenant
	}
	if job.RequesterUserID == "" {
		return contentActor{}, refuseGrant(http.StatusConflict, "the job has no human requester")
	}
	if m, ok := s.roomMember(job.RequesterUserID, roomID); !ok || !m.MayChat() {
		return contentActor{}, refuseGrant(http.StatusForbidden, "the job's requester may no longer post in the room")
	}
	acting := auth.Claims{Subject: job.RequesterUserID, Tenant: job.TenantID, Rooms: []string{roomID}}
	return contentActor{Claims: acting, Room: roomID, Job: &job, Bridge: caller.Subject}, nil
}

// contentCaller resolves a content request's actor (contentGrant) under the
// route's grant mode. The route table has already authenticated the caller,
// applied the route's org permission and rate budget, and checked the
// content store. It answers 400/403/404/409/503 itself.
func (s *Service) contentCaller(w http.ResponseWriter, r *http.Request, caller auth.Claims, grant jobGrant) (contentActor, bool) {
	if grant == grantRefused && strings.TrimSpace(r.URL.Query().Get("job")) != "" {
		httpx.Error(w, "this content action is never taken on behalf of a job", http.StatusBadRequest)
		return contentActor{}, false
	}
	actor, err := s.contentGrant(r, caller)
	if err != nil {
		writeGrantErr(w, err)
		return contentActor{}, false
	}
	return actor, true
}

// jobSaveAllowed applies the save rule of a granted request: a borrowed or
// platform agent saves no script-bearing package. It answers the refusal.
func jobSaveAllowed(w http.ResponseWriter, actor contentActor, pkg *contentpkg.Package) bool {
	if actor.Job == nil || actor.Job.OwnerUserID == actor.Job.RequesterUserID {
		return true
	}
	if pkg.Scripts || pkg.Manifest.NeedsSandbox() {
		httpx.Error(w, "a package with scripts is saved only from your own agent: this agent belongs to someone else. Save it from the reply's files (\"Save as expert\" / \"Save as skill pack\") after reviewing it", http.StatusForbidden)
		return false
	}
	return true
}

// auditJobSave records a component saved by an agent on behalf of a job's
// requester. Best-effort, like the expert use audit.
func (s *Service) auditJobSave(ctx context.Context, actor contentActor, c store.ContentComponent, version string) {
	if actor.Job == nil {
		return
	}
	j := actor.Job
	agent := jobAgent(*j)
	s.audit(ctx, j.TenantID, store.ExtAuditRecord{
		Kind: wire.AuditKindContent, Name: c.Name + "@" + version, Action: "save", Actor: j.RequesterUserID,
		Outcome: wire.AuditOutcomeAllow,
		Detail: shortKind(c.Kind) + " " + c.Name + " v" + version + " saved by agent " + agent + " for " + j.RequesterUserID +
			" (job " + j.JobID + ", room " + j.RoomID + ", bridge of " + actor.Bridge + ")",
	})
}
