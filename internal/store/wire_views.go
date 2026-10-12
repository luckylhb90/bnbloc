package store

import (
	"encoding/json"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The member-facing projections of store rows (decision O4). A service never
// writes a store row to a room member: it writes the row's View, so a column
// added to the row (a tenant, a lease, a blob key) stays server-side unless
// the projection names it.

// View is the job as room members see it (wire.AgentJob): the room events
// and the room's job list. The bridge's raw diagnostics are reduced to the
// fields a member reads.
func (j AgentJob) View() wire.AgentJob {
	var diag struct {
		Error          string   `json:"error"`
		Warning        string   `json:"warning"`
		ContentMissing []string `json:"contentMissing"`
		SessionID      string   `json:"sessionId"`
	}
	if len(j.DiagnosticsJSON) > 0 {
		_ = json.Unmarshal(j.DiagnosticsJSON, &diag) // free-form bridge JSON: a malformed report projects nothing
	}
	return wire.AgentJob{
		JobID:             j.JobID,
		RoomID:            j.RoomID,
		ChannelID:         j.ChannelID,
		MessageSeq:        j.MessageSeq,
		RequesterUserID:   j.RequesterUserID,
		OwnerUserID:       j.OwnerUserID,
		Core:              j.Core,
		Provider:          j.Provider,
		MessageText:       j.MessageText,
		Model:             j.Model,
		ReasoningEffort:   j.ReasoningEffort,
		Status:            string(j.Status),
		FailureReason:     string(j.FailureReason),
		Attempt:           j.Attempt,
		CreatedAtMs:       j.CreatedAtMs,
		UpdatedAtMs:       j.UpdatedAtMs,
		ClaimedAtMs:       j.ClaimedAtMs,
		StartedAtMs:       j.StartedAtMs,
		FinishedAtMs:      j.FinishedAtMs,
		DispatchedByJobID: j.DispatchedByJobID,
		DispatchDepth:     j.DispatchDepth,
		ExpertID:          j.ExpertID,
		ExpertVersion:     j.ExpertVersion,
		ExpertName:        j.ExpertName,
		RoomExpertID:      j.RoomExpertID,
		RoomExpertVersion: j.RoomExpertVersion,
		RoomExpertName:    j.RoomExpertName,
		Error:             diag.Error,
		Warning:           diag.Warning,
		ContentMissing:    diag.ContentMissing,
		ReplySessionID:    diag.SessionID,
	}
}

// AgentJobViews projects a job list.
func AgentJobViews(jobs []AgentJob) []wire.AgentJob {
	out := make([]wire.AgentJob, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.View())
	}
	return out
}

// View is the artifact as room members see it (wire.Artifact).
func (a Artifact) View() wire.Artifact {
	return wire.Artifact{
		ArtifactID:     a.ArtifactID,
		RoomID:         a.RoomID,
		ChannelID:      a.ChannelID,
		Kind:           a.Kind,
		Title:          a.Title,
		Language:       a.Language,
		MimeType:       a.MimeType,
		FileName:       a.FileName,
		CurrentVersion: a.CurrentVersion,
		CreatedBy:      a.CreatedBy,
		CreatedAtMs:    a.CreatedAtMs,
		UpdatedAtMs:    a.UpdatedAtMs,
	}
}

// ArtifactViews projects an artifact list.
func ArtifactViews(arts []Artifact) []wire.Artifact {
	out := make([]wire.Artifact, 0, len(arts))
	for _, a := range arts {
		out = append(out, a.View())
	}
	return out
}

// View is the version as room members see it (wire.ArtifactVersion): no
// tenant and no blob key (the bytes are served by the content route).
func (v ArtifactVersion) View() wire.ArtifactVersion {
	out := wire.ArtifactVersion{
		ArtifactID:    v.ArtifactID,
		Version:       v.Version,
		ParentVersion: v.ParentVersion,
		ContentHash:   v.ContentHash,
		Content:       v.Content,
		Storage:       v.Storage,
		SizeBytes:     v.SizeBytes,
		MimeType:      v.MimeType,
		SourceJobID:   v.SourceJobID,
		SourceSeq:     v.SourceSeq,
		Summary:       v.Summary,
		AuthorUserID:  v.AuthorUserID,
		CreatedAtMs:   v.CreatedAtMs,
	}
	if v.Thumbnail != nil {
		out.Thumbnail = &wire.ArtifactThumbnail{MimeType: v.Thumbnail.MimeType, Width: v.Thumbnail.Width, Height: v.Thumbnail.Height}
	}
	return out
}

// ArtifactVersionViews projects a version list.
func ArtifactVersionViews(vs []ArtifactVersion) []wire.ArtifactVersion {
	out := make([]wire.ArtifactVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.View())
	}
	return out
}
