package contentuse

import (
	"context"
	"errors"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// Staged drafts (docs/prd/2026-10-12-experts-and-teams.md, "Runtime gap
// closure" 4): a job dispatched by components try records the staged draft as
// its expert (an id with store.ContentDraftIDPrefix). It resolves only for
// that job: a draft the job's parent staged, in the job's room and tenant,
// before it expires. Its trust owner is the draft's author (the agent owner
// whose agent wrote it), so someone else's script-bearing draft forces the
// sandbox like any of their content.

// errDraftGone: the job's draft no longer resolves (expired, deleted, or
// not staged for this job).
var errDraftGone = errors.New("draft not available")

// jobDraft resolves the staged draft a job wears as the component and
// version Ref and the requirement resolution read.
func jobDraft(ctx context.Context, st store.ContentComponentStore, job store.AgentJob, nowMs int64) (store.ContentComponent, store.ContentComponentVersion, error) {
	d, err := st.GetContentDraft(ctx, job.ExpertID)
	if errors.Is(err, store.ErrContentNotFound) {
		return store.ContentComponent{}, store.ContentComponentVersion{}, errDraftGone
	}
	if err != nil {
		return store.ContentComponent{}, store.ContentComponentVersion{}, err
	}
	if d.TenantID != job.TenantID || d.RoomID != job.RoomID || d.JobID == "" || d.JobID != job.DispatchedByJobID || !d.Live(nowMs) {
		return store.ContentComponent{}, store.ContentComponentVersion{}, errDraftGone
	}
	c := store.ContentComponent{ID: d.ID, Kind: d.Kind, Name: d.Name, TenantID: d.TenantID, Owner: d.Author,
		DisplayName: d.DisplayName, LatestVersion: d.Version}
	v := store.ContentComponentVersion{ComponentID: d.ID, Version: d.Version, ManifestJSON: d.ManifestJSON,
		BundleBlobKey: d.BundleBlobKey, SHA256: d.SHA256, SizeBytes: d.SizeBytes, CreatedBy: d.Requester, CreatedAtMs: d.CreatedAtMs}
	return c, v, nil
}

// jobExpert resolves the expert a job pinned: a staged draft, or a
// component the requester may use (Usable). ErrNotUsable when neither
// resolves any more.
func jobExpert(ctx context.Context, st store.ContentComponentStore, job store.AgentJob, usable func() (store.ContentComponent, store.ContentComponentVersion, error)) (store.ContentComponent, store.ContentComponentVersion, error) {
	if !store.IsContentDraftID(job.ExpertID) {
		return usable()
	}
	c, v, err := jobDraft(ctx, st, job, time.Now().UnixMilli())
	if errors.Is(err, errDraftGone) {
		return c, v, ErrNotUsable
	}
	return c, v, err
}
