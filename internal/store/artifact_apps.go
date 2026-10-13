package store

import (
	"context"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Which deliverables are interactive apps (docs/prd/2026-10-13-app-
// gallery.md §5): a site whose data space exists (its app key is stored and
// not being destroyed: someone wrote a document through the app and its
// data was not deleted since), or whose current version loads the app SDK
// (~app/sdk.js, detected once when the version's bytes were stored:
// appdata.SDKScan, ArtifactVersion.LoadsAppSDK). Artifact.IsApp is the one
// Go rule and sqlArtifactIsApp its SQL twin, kept here beside it; every
// reader (the artifact projection's isApp, the deliverables' Apps filter,
// a member's apps across rooms, the agents' .room/apps.md through the
// projection) goes through them (TestAppRuleSingleOwner).

// IsApp reports whether the artifact is an interactive app.
func (a Artifact) IsApp() bool {
	return a.Kind == ArtifactKindSite && (a.LoadsAppSDK || a.HasAppData)
}

const (
	// sqlArtifactLoadsSDK is Artifact.LoadsAppSDK over an artifacts row
	// aliased a: its current version's detection.
	sqlArtifactLoadsSDK = `EXISTS (SELECT 1 FROM artifact_versions v WHERE v.tenant_id=a.tenant_id AND v.artifact_id=a.artifact_id AND v.version=a.current_version AND v.loads_app_sdk=1)`
	// sqlArtifactHasAppData is Artifact.HasAppData: the app's key is
	// stored and not being destroyed (a wipe in progress empties it first).
	sqlArtifactHasAppData = `EXISTS (SELECT 1 FROM app_keys k WHERE k.app_id=a.artifact_id AND k.tenant_id=a.tenant_id AND k.wrapped_key<>'')`
	// sqlArtifactIsApp is Artifact.IsApp.
	sqlArtifactIsApp = `(a.kind='` + ArtifactKindSite + `' AND (` + sqlArtifactLoadsSDK + ` OR ` + sqlArtifactHasAppData + `))`
)

// AppListQuery pages the apps of a set of rooms of the ctx tenant,
// newest-updated first, with keyset paging on (UpdatedAtMs, ArtifactID)
// like ArtifactQuery. A page holds clampLimit(Limit, DefaultAppListPage,
// MaxAppListPage) apps.
type AppListQuery struct {
	// RoomIDs are the rooms to list (none: an empty page).
	RoomIDs         []string
	BeforeUpdatedMs int64
	BeforeID        string
	Limit           int
}

// App list page sizes, the same on both backends.
const (
	DefaultAppListPage = 24
	MaxAppListPage     = 60
)

// PageSize is the page q reads.
func (q AppListQuery) PageSize() int { return clampLimit(q.Limit, DefaultAppListPage, MaxAppListPage) }

// after reports whether a sorts after q's cursor (newest-updated first).
func (q AppListQuery) after(a Artifact) bool {
	if q.BeforeUpdatedMs <= 0 {
		return true
	}
	return a.UpdatedAtMs < q.BeforeUpdatedMs || (a.UpdatedAtMs == q.BeforeUpdatedMs && a.ArtifactID < q.BeforeID)
}

// withAppFactsLocked is a with the two app facts the memory store keeps
// apart: its current version's SDK detection and whether its data space
// exists (the app docs store wired in by NewBackends; none: no data).
func (s *MemoryArtifacts) withAppFactsLocked(a Artifact) Artifact {
	a.LoadsAppSDK = s.versions[a.ArtifactID][a.CurrentVersion].LoadsAppSDK
	a.HasAppData = s.appData != nil && s.appData.hasDataSpace(a.TenantID, a.ArtifactID)
	return a
}

// ListApps returns the apps of q's rooms (see AppListQuery).
func (s *MemoryArtifacts) ListApps(ctx context.Context, q AppListQuery) ([]Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out := []Artifact{}
	if len(q.RoomIDs) == 0 {
		return out, nil
	}
	in := make(map[string]bool, len(q.RoomIDs))
	for _, id := range q.RoomIDs {
		in[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.artifacts {
		if a.TenantID != tid || !in[a.RoomID] || !q.after(a) {
			continue
		}
		if a = s.withAppFactsLocked(a); a.IsApp() {
			out = append(out, a)
		}
	}
	sortArtifactsNewestFirst(out)
	if n := q.PageSize(); len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// hasDataSpace reports whether app appID of tenant tid has a data key.
func (s *MemoryAppDocs) hasDataSpace(tid, appID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[appID]
	return ok && k.TenantID == tid && k.WrappedKey != ""
}

// ListApps returns the apps of q's rooms (see AppListQuery).
func (m *MySQLArtifacts) ListApps(ctx context.Context, q AppListQuery) ([]Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if len(q.RoomIDs) == 0 {
		return []Artifact{}, nil
	}
	rooms := append([]string(nil), q.RoomIDs...)
	sort.Strings(rooms)
	args := []any{tid}
	for _, id := range rooms {
		args = append(args, id)
	}
	sqlq := `SELECT ` + artifactCols + ` FROM artifacts a WHERE a.tenant_id=? AND a.room_id IN (` + inPlaceholders(len(rooms)) + `) AND ` + sqlArtifactIsApp
	if q.BeforeUpdatedMs > 0 {
		sqlq += ` AND (a.updated_at_ms < ? OR (a.updated_at_ms = ? AND a.artifact_id < ?))`
		args = append(args, q.BeforeUpdatedMs, q.BeforeUpdatedMs, q.BeforeID)
	}
	sqlq += ` ORDER BY a.updated_at_ms DESC, a.artifact_id DESC LIMIT ?`
	return queryAll(ctx, m.DB, sqlq, scanArtifact, append(args, q.PageSize())...)
}
