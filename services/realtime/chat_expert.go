package realtimehttp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Experts on a chat (docs/prd/2026-10-12-experts-and-teams.md §2 "Using
// it"): a message to an agent may ask it to wear an expert. The expert is
// resolved here, as the author, and its version pinned on the job, so the
// run record and the bridge agree on exactly what was worn.

var (
	// errExpertNotAvailable: the expert does not exist or the author may not
	// use it in this room (400).
	errExpertNotAvailable = errors.New("expert not available")
	// errExpertWithoutAgent: an expert on a message to no agent (400).
	errExpertWithoutAgent = errors.New("an expert needs an agent recipient")
	// errDraftNotAvailable: the draft does not exist, was not staged by
	// the dispatching job in this room, or expired (400).
	errDraftNotAvailable = errors.New("draft not available")
)

// chatExpert is a resolved chat expert.
type chatExpert struct {
	ID          string
	Name        string
	DisplayName string
	Version     string
	// Draft marks a staged draft worn for a quick try (ID is the draft's).
	Draft bool
}

// label is how the expert reads in records ("Reviewer", "Reviewer
// (draft)").
func (e *chatExpert) label() string {
	name := e.Name
	if e.DisplayName != "" {
		name = e.DisplayName
	}
	if e.Draft {
		return name + " (draft)"
	}
	return name
}

// resolveChatExpert pins the expert a post asks for (nil when none).
func (s *Service) resolveChatExpert(ctx context.Context, p ChatPost, agent bool) (*chatExpert, error) {
	if p.Draft != "" {
		if !agent {
			return nil, errExpertWithoutAgent
		}
		return s.resolveChatDraft(ctx, p)
	}
	if p.Expert == nil || strings.TrimSpace(p.Expert.ID) == "" {
		return nil, nil
	}
	if !agent {
		return nil, errExpertWithoutAgent
	}
	if s.Content == nil {
		return nil, errExpertNotAvailable
	}
	viewer := extpipe.Viewer{Subject: p.Author, Tenant: tenant.FromContext(ctx)}
	c, v, err := contentuse.Usable(ctx, s.Content, viewer, p.RoomID, strings.TrimSpace(p.Expert.ID), strings.TrimSpace(p.Expert.Version))
	if errors.Is(err, contentuse.ErrNotUsable) {
		return nil, errExpertNotAvailable
	}
	if err != nil {
		return nil, fmt.Errorf("expert lookup failed: %w", err)
	}
	m, err := contentuse.Manifest(v)
	if err != nil {
		return nil, fmt.Errorf("expert lookup failed: %w", err)
	}
	if c.Kind != contentpkg.KindExpert {
		return nil, errExpertNotAvailable
	}
	return &chatExpert{ID: c.ID, Name: c.Name, DisplayName: m.DisplayName, Version: v.Version}, nil
}

// resolveChatDraft pins the staged draft a dispatch asks its target to wear
// (a quick try): it must exist in this tenant and room, have been staged by
// the dispatching job (p.Dispatched), and not have expired. The job records
// the draft as its expert; contentuse.Effective resolves it the same way.
func (s *Service) resolveChatDraft(ctx context.Context, p ChatPost) (*chatExpert, error) {
	if s.Content == nil || p.Dispatched == nil || p.Expert != nil {
		return nil, errDraftNotAvailable
	}
	d, err := s.Content.GetContentDraft(ctx, strings.TrimSpace(p.Draft))
	if errors.Is(err, store.ErrContentNotFound) {
		return nil, errDraftNotAvailable
	}
	if err != nil {
		return nil, fmt.Errorf("draft lookup failed: %w", err)
	}
	if d.TenantID != tenant.FromContext(ctx) || d.RoomID != p.RoomID || d.JobID != p.Dispatched.JobID || !d.Live(time.Now().UnixMilli()) {
		return nil, errDraftNotAvailable
	}
	return &chatExpert{ID: d.ID, Name: d.Name, DisplayName: d.DisplayName, Version: d.Version, Draft: true}, nil
}

// payload is the chat message's expert block.
func (e *chatExpert) payload() map[string]any {
	out := map[string]any{"id": e.ID, "name": e.Name, "version": e.Version}
	if e.DisplayName != "" {
		out["displayName"] = e.DisplayName
	}
	if e.Draft {
		out["draft"] = true
	}
	return out
}

// auditExpertUse records that a job asked an agent to wear an expert
// ("expert X v1.2 on agent Y of member Z"). Best-effort: a failed append is
// logged, never fails the post.
func (s *Service) auditExpertUse(ctx context.Context, e *chatExpert, job store.AgentJob) {
	if e == nil || s.Audit == nil {
		return
	}
	agent := job.OwnerUserID + "/" + job.Provider
	if job.Core != "" {
		agent = job.OwnerUserID + "/" + job.Core + "/" + job.Provider
	}
	action, what := "use", "expert "
	if e.Draft {
		// A quick try of an unsaved draft (try_draft): not a use of a
		// component.
		action, what = "try", "draft "
	}
	rec := store.ExtAuditRecord{
		Kind: wire.AuditKindContent, Name: e.Name + "@" + e.Version, Action: action, Actor: job.RequesterUserID,
		TenantID: tenant.FromContext(ctx), Outcome: "queued",
		Detail: what + e.label() + " v" + e.Version + " on agent " + agent + " (job " + job.JobID + ", room " + job.RoomID + ")",
		AtMs:   time.Now().UnixMilli(),
	}
	if err := s.Audit.AppendExtAudit(ctx, rec); err != nil {
		log.Printf("realtime: audit expert use job=%s: %v", job.JobID, err)
	}
}
