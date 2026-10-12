package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Content requests on behalf of a running job's requester
// (docs/prd/2026-10-12-experts-and-teams.md §5, docs/architecture/
// room-tools.md): the room tools' save_component, get_component and
// list_components call the ordinary content routes from the bridge running
// a job, with a ?job=&room= grant. The authority model is dispatch's: the
// bridge never acts as itself or its owner; a grant is admitted only while
// the job is live and leased by a bridge of the caller (store.JobLeasedBy)
// and its requester is still a human member of the room, and the request
// then acts as that requester: what they may read is read, and a save lands
// in their own Library (personal scope, owner = the requester).
//
// One extra rule applies to saves: when the agent is not the requester's
// own (a borrowed or platform agent), a package that ships scripts is
// refused. Someone else's machine may not plant executable content into a
// member's own Library, where "own content" runs unsandboxed; the member
// can still save it explicitly from the turn's deliverables.

// contentActor is who a content request acts as.
type contentActor struct {
	// Claims is the acting member: the caller, or a job's requester.
	auth.Claims
	// Job is the job of a granted bridge request (nil for a member's own
	// request).
	Job *store.AgentJob
	// Bridge is the subject whose bridge made a granted request.
	Bridge string
}

// contentActing resolves the actor of a content request. Without a job
// grant the caller acts as themselves. It answers refusals itself.
func (s *Service) contentActing(w http.ResponseWriter, r *http.Request, caller auth.Claims) (contentActor, bool) {
	q := r.URL.Query()
	jobID := strings.TrimSpace(q.Get("job"))
	if jobID == "" {
		return contentActor{Claims: caller}, true
	}
	roomID := strings.TrimSpace(q.Get("room"))
	job, ok := s.contentJob(w, r, caller, jobID, roomID)
	if !ok {
		return contentActor{}, false
	}
	if job.RequesterUserID == "" {
		http.Error(w, "the job has no human requester", http.StatusConflict)
		return contentActor{}, false
	}
	if !s.humanRoomMember(job.RequesterUserID, roomID) {
		http.Error(w, "the job's requester is no longer a member of the room", http.StatusForbidden)
		return contentActor{}, false
	}
	acting := auth.Claims{Subject: job.RequesterUserID, Tenant: job.TenantID, Rooms: []string{roomID}}
	return contentActor{Claims: acting, Job: &job, Bridge: caller.Subject}, true
}

// humanRoomMember reports whether subject is a human member of the room.
func (s *Service) humanRoomMember(subject, roomID string) bool {
	if s.RoomRole == nil {
		return false
	}
	role, human := s.RoomRole(subject, roomID)
	return human && role != ""
}

// jobSaveAllowed applies the save rule of a granted request: a borrowed or
// platform agent saves no script-bearing package. It answers the refusal.
func jobSaveAllowed(w http.ResponseWriter, actor contentActor, pkg *contentpkg.Package) bool {
	if actor.Job == nil || actor.Job.OwnerUserID == actor.Job.RequesterUserID {
		return true
	}
	if pkg.Scripts || pkg.Manifest.NeedsSandbox() {
		http.Error(w, "a package with scripts is saved only from your own agent: this agent belongs to someone else. Save it from the reply's files (\"Save as expert\" / \"Save as skill pack\") after reviewing it", http.StatusForbidden)
		return false
	}
	return true
}

// auditJobSave records a component saved by an agent on behalf of a job's
// requester. Best-effort, like the expert use audit.
func (s *Service) auditJobSave(ctx context.Context, actor contentActor, c store.ContentComponent, version string) {
	if actor.Job == nil || s.ExtAudit == nil {
		return
	}
	j := actor.Job
	agent := jobAgent(*j)
	_ = s.ExtAudit.AppendExtAudit(ctx, store.ExtAuditRecord{
		Kind: wire.AuditKindContent, Name: c.Name + "@" + version, Action: "save", Actor: j.RequesterUserID,
		TenantID: j.TenantID, Outcome: "allow",
		Detail: shortKind(c.Kind) + " " + c.Name + " v" + version + " saved by agent " + agent + " for " + j.RequesterUserID +
			" (job " + j.JobID + ", room " + j.RoomID + ", bridge of " + actor.Bridge + ")",
		AtMs: s.nowTime().UnixMilli(),
	})
}
