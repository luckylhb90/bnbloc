package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Content components (docs/prd/2026-10-12-experts-and-teams.md): member-
// and platform-authored packages that are mostly text (experts, skill
// packs, prompt packs). They are numerous, so they live in their own tables
// rather than as platform_settings records like the executable kinds:
// content_components (one row per component), content_component_versions
// (immutable versions; the bundle is a blob addressed by sha256),
// content_component_uses (where a component is enabled or pinned: for a
// member, for a room) and content_permission_acks (a member confirmed a
// version's declared permissions).

// ContentScope is who sees a content component.
type ContentScope string

const (
	// ContentScopePersonal: the author only.
	ContentScopePersonal ContentScope = "personal"
	// ContentScopeShared: a copy pinned to a version for named members or a
	// room (a later delivery step).
	ContentScopeShared ContentScope = "shared"
	// ContentScopeOrg: the organization, after an org admin approved it.
	ContentScopeOrg ContentScope = "org"
	// ContentScopePlatform: every organization (rollout + tenant scope).
	ContentScopePlatform ContentScope = "platform"
)

// ContentStatus is a component's publication state within its scope.
type ContentStatus string

const (
	ContentStatusActive ContentStatus = "active"
	// ContentStatusPending: waiting for approval at org scope.
	ContentStatusPending ContentStatus = "pending"
	// ContentStatusRetired: hidden from new use; pinned users keep theirs.
	ContentStatusRetired ContentStatus = "retired"
)

// ContentComponent is one content component. DisplayName, Description and
// Category are the latest version's, denormalized for listing.
type ContentComponent struct {
	ID            string
	Kind          string
	Name          string
	TenantID      string
	Owner         string
	Scope         ContentScope
	Status        ContentStatus
	LatestVersion string
	DisplayName   string
	Description   string
	Category      string
	CreatedAtMs   int64
	UpdatedAtMs   int64
}

// ContentComponentVersion is one immutable version: its manifest and the
// bundle blob (key + sha256 of the archive).
type ContentComponentVersion struct {
	ComponentID   string
	Version       string
	ManifestJSON  json.RawMessage
	BundleBlobKey string
	SHA256        string
	SizeBytes     int64
	// Signature is the platform signature of a platform-scope version
	// (empty for member content: server validation and the pinned hash are
	// its trust anchor).
	Signature   string
	CreatedBy   string
	CreatedAtMs int64
}

// ContentUseScope is where a component is enabled.
type ContentUseScope string

const (
	// ContentUseMember: for a member, across all their agents (skill packs).
	ContentUseMember ContentUseScope = "member"
	// ContentUseRoom: for a room — room skill packs (every agent in the
	// room) and pinned experts (offered in the room's composer).
	ContentUseRoom ContentUseScope = "room"
)

// ContentUse enables or pins a component in a scope, at a pinned version
// or following the latest ("").
type ContentUse struct {
	TenantID      string
	ScopeKind     ContentUseScope
	ScopeID       string // member subject or room id
	ComponentID   string
	Kind          string
	PinnedVersion string
	CreatedBy     string
	CreatedAtMs   int64
}

// ContentAck records that a member confirmed a version's declared
// permissions (first use).
type ContentAck struct {
	Subject     string
	ComponentID string
	Version     string
	AckedAtMs   int64
}

// ContentComponentQuery filters a listing. Zero fields do not filter; a
// listing is bounded by Limit (MaxContentList when zero or larger).
type ContentComponentQuery struct {
	TenantID string
	Owner    string
	Kind     string
	Scopes   []ContentScope
	IDs      []string
	Limit    int
}

// MaxContentList bounds one listing.
const MaxContentList = 500

var (
	// ErrContentNotFound: no such component or version.
	ErrContentNotFound = errors.New("store: content component not found")
	// ErrContentExists: the scope already has a component of that kind and
	// name.
	ErrContentExists = errors.New("store: content component exists")
	// ErrContentVersionConflict: the version exists, or latest moved under
	// a concurrent save.
	ErrContentVersionConflict = errors.New("store: content component version conflict")
)

// ContentComponentStore persists content components. Implementations are
// safe for concurrent use.
type ContentComponentStore interface {
	// CreateContentComponent inserts a component with its first version
	// (ErrContentExists on a name clash in the scope).
	CreateContentComponent(ctx context.Context, c ContentComponent, v ContentComponentVersion) error
	// AddContentComponentVersion appends version v when the component's
	// latest version is still prevLatest, and moves latest to it with the
	// listing fields of c (ErrContentVersionConflict otherwise).
	AddContentComponentVersion(ctx context.Context, c ContentComponent, prevLatest string, v ContentComponentVersion) error
	GetContentComponent(ctx context.Context, id string) (ContentComponent, error)
	ListContentComponents(ctx context.Context, q ContentComponentQuery) ([]ContentComponent, error)
	// ListContentComponentVersions returns a component's versions, newest
	// first.
	ListContentComponentVersions(ctx context.Context, componentID string) ([]ContentComponentVersion, error)
	GetContentComponentVersion(ctx context.Context, componentID, version string) (ContentComponentVersion, error)
	// ContentVersionsByBlob returns the versions whose bundle is key.
	ContentVersionsByBlob(ctx context.Context, key string) ([]ContentComponentVersion, error)
	// DeleteContentComponent removes a component, its versions, uses and
	// acks (the bundles become unreferenced; blob GC collects them).
	DeleteContentComponent(ctx context.Context, id string) error

	// PutContentUse upserts a use by (scope kind, scope id, component).
	PutContentUse(ctx context.Context, u ContentUse) error
	DeleteContentUse(ctx context.Context, scopeKind ContentUseScope, scopeID, componentID string) error
	// ListContentUses returns a scope's uses, oldest first.
	ListContentUses(ctx context.Context, scopeKind ContentUseScope, scopeID string) ([]ContentUse, error)
	// CountContentUses returns, per component, how many scopes use it.
	CountContentUses(ctx context.Context, componentIDs []string) (map[string]int, error)

	PutContentAck(ctx context.Context, a ContentAck) error
	// ListContentAcks returns a member's acks of the given components.
	ListContentAcks(ctx context.Context, subject string, componentIDs []string) ([]ContentAck, error)
}

func contentLimit(n int) int {
	if n <= 0 || n > MaxContentList {
		return MaxContentList
	}
	return n
}

func (q ContentComponentQuery) admits(c ContentComponent) bool {
	if q.TenantID != "" && c.TenantID != q.TenantID {
		return false
	}
	if q.Owner != "" && c.Owner != q.Owner {
		return false
	}
	if q.Kind != "" && c.Kind != q.Kind {
		return false
	}
	if len(q.Scopes) > 0 {
		ok := false
		for _, s := range q.Scopes {
			ok = ok || c.Scope == s
		}
		if !ok {
			return false
		}
	}
	if len(q.IDs) > 0 {
		ok := false
		for _, id := range q.IDs {
			ok = ok || c.ID == id
		}
		if !ok {
			return false
		}
	}
	return true
}

// MemoryContentComponents is the in-memory ContentComponentStore.
type MemoryContentComponents struct {
	mu         sync.Mutex
	components map[string]ContentComponent
	versions   map[string][]ContentComponentVersion // component -> oldest first
	uses       map[string]ContentUse                // scope kind\x00scope id\x00component
	acks       map[string]ContentAck                // subject\x00component\x00version
}

// NewMemoryContentComponents returns an empty in-memory store.
func NewMemoryContentComponents() *MemoryContentComponents {
	return &MemoryContentComponents{components: map[string]ContentComponent{}, versions: map[string][]ContentComponentVersion{}, uses: map[string]ContentUse{}, acks: map[string]ContentAck{}}
}

var _ ContentComponentStore = (*MemoryContentComponents)(nil)

func contentNameKey(c ContentComponent) string {
	return c.TenantID + "\x00" + string(c.Scope) + "\x00" + c.Owner + "\x00" + c.Kind + "\x00" + c.Name
}

func (s *MemoryContentComponents) CreateContentComponent(_ context.Context, c ContentComponent, v ContentComponentVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.components[c.ID]; ok {
		return ErrContentExists
	}
	for _, other := range s.components {
		if contentNameKey(other) == contentNameKey(c) {
			return ErrContentExists
		}
	}
	c.LatestVersion = v.Version
	s.components[c.ID] = c
	v.ComponentID = c.ID
	s.versions[c.ID] = []ContentComponentVersion{v}
	return nil
}

func (s *MemoryContentComponents) AddContentComponentVersion(_ context.Context, c ContentComponent, prevLatest string, v ContentComponentVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.components[c.ID]
	if !ok {
		return ErrContentNotFound
	}
	if cur.LatestVersion != prevLatest {
		return ErrContentVersionConflict
	}
	for _, have := range s.versions[c.ID] {
		if have.Version == v.Version {
			return ErrContentVersionConflict
		}
	}
	cur.LatestVersion, cur.DisplayName, cur.Description, cur.Category, cur.UpdatedAtMs = v.Version, c.DisplayName, c.Description, c.Category, c.UpdatedAtMs
	s.components[c.ID] = cur
	v.ComponentID = c.ID
	s.versions[c.ID] = append(s.versions[c.ID], v)
	return nil
}

func (s *MemoryContentComponents) GetContentComponent(_ context.Context, id string) (ContentComponent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.components[id]
	if !ok {
		return ContentComponent{}, ErrContentNotFound
	}
	return c, nil
}

func (s *MemoryContentComponents) ListContentComponents(_ context.Context, q ContentComponentQuery) ([]ContentComponent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ContentComponent{}
	for _, c := range s.components {
		if q.admits(c) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAtMs != out[j].UpdatedAtMs {
			return out[i].UpdatedAtMs > out[j].UpdatedAtMs
		}
		return out[i].ID < out[j].ID
	})
	if n := contentLimit(q.Limit); len(out) > n {
		out = out[:n]
	}
	return out, nil
}

func (s *MemoryContentComponents) ListContentComponentVersions(_ context.Context, componentID string) ([]ContentComponentVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := s.versions[componentID]
	out := make([]ContentComponentVersion, 0, len(vs))
	for i := len(vs) - 1; i >= 0; i-- {
		out = append(out, vs[i])
	}
	return out, nil
}

func (s *MemoryContentComponents) GetContentComponentVersion(_ context.Context, componentID, version string) (ContentComponentVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.versions[componentID] {
		if v.Version == version {
			return v, nil
		}
	}
	return ContentComponentVersion{}, ErrContentNotFound
}

func (s *MemoryContentComponents) ContentVersionsByBlob(_ context.Context, key string) ([]ContentComponentVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ContentComponentVersion{}
	for _, vs := range s.versions {
		for _, v := range vs {
			if v.BundleBlobKey == key {
				out = append(out, v)
			}
		}
	}
	return out, nil
}

// referencedBlobs reports which keys a version still references.
func (s *MemoryContentComponents) referencedBlobs(keys map[string]bool, out map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, vs := range s.versions {
		for _, v := range vs {
			if keys[v.BundleBlobKey] {
				out[v.BundleBlobKey] = true
			}
		}
	}
}

func (s *MemoryContentComponents) DeleteContentComponent(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.components, id)
	delete(s.versions, id)
	for k, u := range s.uses {
		if u.ComponentID == id {
			delete(s.uses, k)
		}
	}
	for k, a := range s.acks {
		if a.ComponentID == id {
			delete(s.acks, k)
		}
	}
	return nil
}

func contentUseKey(scopeKind ContentUseScope, scopeID, componentID string) string {
	return string(scopeKind) + "\x00" + scopeID + "\x00" + componentID
}

func (s *MemoryContentComponents) PutContentUse(_ context.Context, u ContentUse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.components[u.ComponentID]; !ok {
		return ErrContentNotFound
	}
	k := contentUseKey(u.ScopeKind, u.ScopeID, u.ComponentID)
	if prev, ok := s.uses[k]; ok {
		u.CreatedAtMs, u.CreatedBy = prev.CreatedAtMs, prev.CreatedBy
	}
	s.uses[k] = u
	return nil
}

func (s *MemoryContentComponents) DeleteContentUse(_ context.Context, scopeKind ContentUseScope, scopeID, componentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.uses, contentUseKey(scopeKind, scopeID, componentID))
	return nil
}

func (s *MemoryContentComponents) ListContentUses(_ context.Context, scopeKind ContentUseScope, scopeID string) ([]ContentUse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ContentUse{}
	for _, u := range s.uses {
		if u.ScopeKind == scopeKind && u.ScopeID == scopeID {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs < out[j].CreatedAtMs
		}
		return out[i].ComponentID < out[j].ComponentID
	})
	return out, nil
}

func (s *MemoryContentComponents) CountContentUses(_ context.Context, componentIDs []string) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, id := range componentIDs {
		want[id] = true
	}
	out := map[string]int{}
	for _, u := range s.uses {
		if want[u.ComponentID] {
			out[u.ComponentID]++
		}
	}
	return out, nil
}

func (s *MemoryContentComponents) PutContentAck(_ context.Context, a ContentAck) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := a.Subject + "\x00" + a.ComponentID + "\x00" + a.Version
	if _, ok := s.acks[k]; !ok {
		s.acks[k] = a
	}
	return nil
}

func (s *MemoryContentComponents) ListContentAcks(_ context.Context, subject string, componentIDs []string) ([]ContentAck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, id := range componentIDs {
		want[id] = true
	}
	out := []ContentAck{}
	for _, a := range s.acks {
		if a.Subject == subject && want[a.ComponentID] {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Compare(out[i].ComponentID+out[i].Version, out[j].ComponentID+out[j].Version) < 0
	})
	return out, nil
}
