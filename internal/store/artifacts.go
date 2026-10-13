package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

var ErrArtifactNotFound = errors.New("store: artifact not found")
var ErrArtifactVersionConflict = errors.New("store: artifact version conflict")

// ErrArtifactIDTaken is PutArtifact's refusal of an id another room (or
// another tenant) already holds: a caller-chosen id never reaches a row
// outside the caller's room.
var ErrArtifactIDTaken = errors.New("store: artifact id is taken by another room")

// ErrInvalidArtifactVersion reports a version whose storage fields contradict
// each other (a blob version without a blob key, an inline one with one).
var ErrInvalidArtifactVersion = errors.New("store: invalid artifact version")

// Artifact kinds supported by the preview surface. The kind is the coarse
// grouping (filters, default viewer); the version's mimeType picks the exact
// viewer. html renders as source (executing uploaded markup is a distinct,
// sandboxed surface, not a renderer).
const (
	ArtifactKindMarkdown = "markdown"
	ArtifactKindCode     = "code"
	// ArtifactKindImage is a raster or vector image: a blob version carrying
	// the image bytes (older inline image versions carry a URL as text).
	ArtifactKindImage = "image"
	ArtifactKindHTML  = "html"
	// ArtifactKindApp is the live kind: a self-contained document meant to be
	// EXECUTED in the sandboxed room runner (interactive diagram, game,
	// dashboard) rather than read. html remains source text — the distinction
	// is the author's intent, and the runner only ever executes kind app.
	ArtifactKindApp = "app"
	// ArtifactKindFile is any other deliverable file (PDF, archive, audio,
	// video, office document, unknown binary): always blob-backed, previewed
	// by MIME type or offered as a download.
	ArtifactKindFile = "file"
	// ArtifactKindSite is a static web app: every version is a blob holding a
	// tar snapshot of the site's directory (tarbundle.SiteMIME), served file
	// by file under a sandboxed site origin, never as one document.
	ArtifactKindSite = "site"
)

// Artifact version storage: inline text in the version row (the original
// model), or bytes in the content-addressed blob store.
const (
	ArtifactStorageInline = "inline"
	ArtifactStorageBlob   = "blob"
)

// Thumbnail sources, lowest priority first. A version keeps the first stored
// thumbnail; a later write replaces it only when it comes from a strictly
// higher-priority source (an installed previewer's rendering beats the
// built-in fallback a client generated first).
const (
	ArtifactThumbnailBuiltin   = "builtin"
	ArtifactThumbnailPreviewer = "previewer"
)

// ArtifactThumbnailRank orders thumbnail sources; 0 = unknown source.
func ArtifactThumbnailRank(source string) int {
	switch source {
	case ArtifactThumbnailBuiltin:
		return 1
	case ArtifactThumbnailPreviewer:
		return 2
	}
	return 0
}

// Artifact is a room-scoped collaboration deliverable: a spec, report, code
// file, image, or any other file the room's members and agents iterate on
// together. It is a first-class server object (not a peer-synced file) so every
// member — including web clients with no local bridge — sees the same current
// version.
type Artifact struct {
	ArtifactID string `json:"artifactId"`
	TenantID   string `json:"tenantId"`
	RoomID     string `json:"roomId"`
	ChannelID  string `json:"channelId"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Language   string `json:"language,omitempty"`
	// MimeType is the current version's media type (empty for artifacts whose
	// versions predate typed versions).
	MimeType string `json:"mimeType,omitempty"`
	// FileName is the display name and extension source ("report.pdf").
	FileName       string `json:"fileName,omitempty"`
	CurrentVersion int    `json:"currentVersion"`
	CreatedBy      string `json:"createdBy"`
	CreatedAtMs    int64  `json:"createdAtMs"`
	UpdatedAtMs    int64  `json:"updatedAtMs"`
	// LoadsAppSDK (the current version's) and HasAppData (its data space
	// exists) are the facts Artifact.IsApp decides on; every read fills
	// them, no write takes them (artifact_apps.go).
	LoadsAppSDK bool `json:"-"`
	HasAppData  bool `json:"-"`
}

// ArtifactAnchor links a room message (Seq, a version's sourceSeq) to an
// artifact one of whose versions that message's turn produced.
type ArtifactAnchor struct {
	Seq        int64  `json:"seq"`
	ArtifactID string `json:"artifactId"`
}

// MaxArtifactAnchors bounds one anchors answer.
const MaxArtifactAnchors = 2000

// ArtifactThumbnail describes a stored version thumbnail (the bytes are served
// by the thumbnail route).
type ArtifactThumbnail struct {
	MimeType string `json:"mimeType"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// ArtifactVersion is one immutable revision of an artifact. Content is
// addressed by hash for integrity; the author and summary give the version
// timeline a reviewable history, which is what "deliverable" means to an
// enterprise room: who changed what, when, and why.
type ArtifactVersion struct {
	ArtifactID    string `json:"artifactId"`
	TenantID      string `json:"tenantId"`
	Version       int    `json:"version"`
	ParentVersion int    `json:"parentVersion"`
	// ContentHash is the sha256 of the version's bytes (inline text or blob).
	ContentHash string `json:"contentHash"`
	// Content is the full inline revision. List responses omit it (metadata
	// only); it is served per version on demand. Empty for blob versions.
	Content string `json:"content,omitempty"`
	// Storage is ArtifactStorageInline or ArtifactStorageBlob.
	Storage string `json:"storage"`
	// BlobKey addresses a blob version's bytes in the blob store.
	BlobKey   string `json:"blobKey,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	MimeType  string `json:"mimeType,omitempty"`
	// SourceJobID / SourceSeq link the version to the agent job and the room
	// message (the job's prompt) that produced it; zero for manual publishes.
	SourceJobID  string `json:"sourceJobId,omitempty"`
	SourceSeq    int64  `json:"sourceSeq,omitempty"`
	Summary      string `json:"summary,omitempty"`
	AuthorUserID string `json:"authorUserId"`
	CreatedAtMs  int64  `json:"createdAtMs"`
	// Thumbnail is present when one was stored for this version.
	Thumbnail *ArtifactThumbnail `json:"thumbnail,omitempty"`
	// ThumbnailBlobKey / ThumbnailRank are the stored thumbnail's location
	// and source priority; server-side only.
	ThumbnailBlobKey string `json:"-"`
	ThumbnailRank    int    `json:"-"`
	// LoadsAppSDK records that the version's site bundle loads the app SDK,
	// detected once when its bytes were stored (appdata.SDKScan); the
	// artifact's IsApp reads its current version's.
	LoadsAppSDK bool `json:"-"`
}

// ArtifactThumbnailWrite is one thumbnail store request.
type ArtifactThumbnailWrite struct {
	BlobKey  string
	MimeType string
	Width    int
	Height   int
	// Rank is ArtifactThumbnailRank of the writer's source.
	Rank int
}

// ArtifactQuery selects a room's artifacts, newest-updated first, with keyset
// paging on (UpdatedAtMs, ArtifactID). A page holds clampLimit(Limit,
// DefaultArtifactListPage, MaxArtifactListPage) artifacts.
type ArtifactQuery struct {
	RoomID    string
	ChannelID string
	Kind      string
	// MimeType matches exactly, or a whole top-level type with "image/*".
	MimeType string
	// Author matches the artifact's creator.
	Author string
	// SourceSeq selects artifacts with a version produced by that message.
	SourceSeq int64
	// Text substring-matches the title or file name (case-insensitive,
	// taken literally).
	Text string
	// Apps selects interactive apps only (Artifact.IsApp).
	Apps bool
	// BeforeUpdatedMs + BeforeID: return only artifacts strictly after this
	// cursor in (updatedAtMs desc, artifactId desc) order. Zero = first page.
	BeforeUpdatedMs int64
	BeforeID        string
	Limit           int
}

// HashArtifactContent addresses artifact content. The server computes it, so
// the value a client sees is an integrity reference, not a claim.
func HashArtifactContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

type ArtifactStore interface {
	// PutArtifact creates an artifact, or returns the stored one unchanged
	// when the id already exists in the same tenant and room (an idempotent
	// attach keeps the version history). ErrArtifactIDTaken when the id
	// belongs to another room or tenant.
	PutArtifact(ctx context.Context, a Artifact) (Artifact, error)
	GetArtifact(ctx context.Context, artifactID string) (Artifact, error)
	ListArtifacts(ctx context.Context, q ArtifactQuery) ([]Artifact, error)
	// ListApps pages the interactive apps of a set of rooms
	// (artifact_apps.go).
	ListApps(ctx context.Context, q AppListQuery) ([]Artifact, error)
	// ArtifactAnchors returns the (sourceSeq, artifact) links of a room's
	// artifact versions whose sourceSeq lies in [fromSeq, toSeq], ordered by
	// seq: the deliverable cards a feed shows under those messages. At most
	// MaxArtifactAnchors rows.
	ArtifactAnchors(ctx context.Context, roomID string, fromSeq, toSeq int64) ([]ArtifactAnchor, error)
	// PublishArtifactVersion appends revision baseVersion+1 (compare-and-swap
	// on the current version). An inline version's hash and size are computed
	// from Content; a blob version carries its blob's ContentHash, BlobKey and
	// SizeBytes. A non-empty MimeType becomes the artifact's current MIME type.
	PublishArtifactVersion(ctx context.Context, artifactID string, v ArtifactVersion, baseVersion int) (Artifact, ArtifactVersion, error)
	GetArtifactVersion(ctx context.Context, artifactID string, version int) (ArtifactVersion, error)
	// ListArtifactVersions returns the newest clampLimit(limit,
	// DefaultArtifactVersionPage, MaxArtifactVersionPage) versions,
	// metadata only (no Content), newest first; [] for an artifact not in
	// the ctx tenant.
	ListArtifactVersions(ctx context.Context, artifactID string, limit int) ([]ArtifactVersion, error)
	// SetArtifactThumbnail stores a version thumbnail when none is stored or
	// the stored one has a lower rank. It returns the version as stored and
	// whether this write landed.
	SetArtifactThumbnail(ctx context.Context, artifactID string, version int, t ArtifactThumbnailWrite) (ArtifactVersion, bool, error)
	// ArtifactIndex: title search across rooms (search.go).
	ArtifactIndex
	// ArtifactShareStore: public share links (artifact_shares.go).
	ArtifactShareStore
}

// prepareArtifactVersion normalizes a version before it is stored: storage
// defaults to inline, an inline version's hash/size derive from its content,
// and a blob version must carry its blob reference and no inline content.
func prepareArtifactVersion(v ArtifactVersion) (ArtifactVersion, error) {
	if v.Storage == "" {
		v.Storage = ArtifactStorageInline
	}
	switch v.Storage {
	case ArtifactStorageInline:
		if v.BlobKey != "" {
			return v, ErrInvalidArtifactVersion
		}
		v.ContentHash = HashArtifactContent(v.Content)
		v.SizeBytes = int64(len(v.Content))
	case ArtifactStorageBlob:
		if v.BlobKey == "" || !digest.Valid(v.ContentHash) || v.Content != "" || v.SizeBytes < 0 {
			return v, ErrInvalidArtifactVersion
		}
	default:
		return v, ErrInvalidArtifactVersion
	}
	v.Thumbnail, v.ThumbnailBlobKey, v.ThumbnailRank = nil, "", 0
	return v, nil
}

// artifactMatches applies an ArtifactQuery's filters except SourceSeq (which
// needs the versions) and paging.
func artifactMatches(a Artifact, q ArtifactQuery) bool {
	if a.RoomID != q.RoomID {
		return false
	}
	if q.ChannelID != "" && a.ChannelID != q.ChannelID {
		return false
	}
	if q.Kind != "" && a.Kind != q.Kind {
		return false
	}
	if q.MimeType != "" {
		if prefix, ok := strings.CutSuffix(q.MimeType, "/*"); ok {
			if !strings.HasPrefix(a.MimeType, prefix+"/") {
				return false
			}
		} else if a.MimeType != q.MimeType {
			return false
		}
	}
	if q.Author != "" && a.CreatedBy != q.Author {
		return false
	}
	if t := strings.TrimSpace(q.Text); t != "" && !containsFold(a.Title, t) && !containsFold(a.FileName, t) {
		return false
	}
	if q.BeforeUpdatedMs > 0 {
		if a.UpdatedAtMs > q.BeforeUpdatedMs || (a.UpdatedAtMs == q.BeforeUpdatedMs && a.ArtifactID >= q.BeforeID) {
			return false
		}
	}
	return true
}

// Artifact page sizes, the same on both backends.
const (
	DefaultArtifactListPage    = 100
	MaxArtifactListPage        = 200
	DefaultArtifactVersionPage = 50
	// MaxArtifactVersionPage covers the handoff export's whole history read.
	MaxArtifactVersionPage = 1000
)

func artifactListLimit(n int) int {
	return clampLimit(n, DefaultArtifactListPage, MaxArtifactListPage)
}

func artifactVersionLimit(n int) int {
	return clampLimit(n, DefaultArtifactVersionPage, MaxArtifactVersionPage)
}

// MemoryArtifacts is the in-memory ArtifactStore (dev / tests).
type MemoryArtifacts struct {
	mu        sync.Mutex
	artifacts map[string]Artifact
	versions  map[string]map[int]ArtifactVersion
	shares    map[string]ArtifactShare
	// appData answers whether an app's data space exists (HasAppData);
	// NewBackends wires the memory app docs store in.
	appData *MemoryAppDocs
}

func NewMemoryArtifacts() *MemoryArtifacts {
	return &MemoryArtifacts{artifacts: map[string]Artifact{}, versions: map[string]map[int]ArtifactVersion{}, shares: map[string]ArtifactShare{}}
}

var _ ArtifactStore = (*MemoryArtifacts)(nil)

func (s *MemoryArtifacts) PutArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Artifact{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a.TenantID = tid
	if existing, ok := s.artifacts[a.ArtifactID]; ok {
		if existing.TenantID != tid || existing.RoomID != a.RoomID {
			return Artifact{}, ErrArtifactIDTaken
		}
		// Idempotent attach: a re-put with the same id keeps the artifact's
		// version history instead of resetting it.
		return s.withAppFactsLocked(existing), nil
	}
	if a.CreatedAtMs <= 0 {
		a.CreatedAtMs = time.Now().UnixMilli()
	}
	if a.UpdatedAtMs <= 0 {
		a.UpdatedAtMs = a.CreatedAtMs
	}
	s.artifacts[a.ArtifactID] = a
	return a, nil
}

func (s *MemoryArtifacts) GetArtifact(ctx context.Context, artifactID string) (Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Artifact{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.artifacts[artifactID]
	if !ok || a.TenantID != tid {
		return Artifact{}, ErrArtifactNotFound
	}
	return s.withAppFactsLocked(a), nil
}

func (s *MemoryArtifacts) ListArtifacts(ctx context.Context, q ArtifactQuery) ([]Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	limit := artifactListLimit(q.Limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Artifact{}
	for _, a := range s.artifacts {
		if a.TenantID != tid || !artifactMatches(a, q) {
			continue
		}
		if q.SourceSeq > 0 && !s.hasSourceSeqLocked(a.ArtifactID, q.SourceSeq) {
			continue
		}
		if a = s.withAppFactsLocked(a); q.Apps && !a.IsApp() {
			continue
		}
		out = append(out, a)
	}
	sortArtifactsNewestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// sortArtifactsNewestFirst is the MySQL artifact list order: updated_at_ms
// DESC, artifact_id DESC.
func sortArtifactsNewestFirst(out []Artifact) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAtMs != out[j].UpdatedAtMs {
			return out[i].UpdatedAtMs > out[j].UpdatedAtMs
		}
		return out[i].ArtifactID > out[j].ArtifactID
	})
}

// ArtifactAnchors returns the room's version links in a seq range (memory
// impl; see ArtifactStore).
func (s *MemoryArtifacts) ArtifactAnchors(ctx context.Context, roomID string, fromSeq, toSeq int64) ([]ArtifactAnchor, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[ArtifactAnchor]bool{}
	out := []ArtifactAnchor{}
	for id, a := range s.artifacts {
		if a.TenantID != tid || a.RoomID != roomID {
			continue
		}
		for _, v := range s.versions[id] {
			k := ArtifactAnchor{Seq: v.SourceSeq, ArtifactID: id}
			if v.SourceSeq > 0 && v.SourceSeq >= fromSeq && v.SourceSeq <= toSeq && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].ArtifactID < out[j].ArtifactID
	})
	if len(out) > MaxArtifactAnchors {
		out = out[:MaxArtifactAnchors]
	}
	return out, nil
}

func (s *MemoryArtifacts) hasSourceSeqLocked(artifactID string, seq int64) bool {
	for _, v := range s.versions[artifactID] {
		if v.SourceSeq == seq {
			return true
		}
	}
	return false
}

// PublishArtifactVersion appends revision baseVersion+1. The base must equal
// the artifact's current version (compare-and-swap): two members or agents
// editing concurrently means the second is refused with the current version so
// it re-reads and rebases, instead of silently interleaving two divergent
// edits — for specs and code a visible conflict beats a woven one.
func (s *MemoryArtifacts) PublishArtifactVersion(ctx context.Context, artifactID string, v ArtifactVersion, baseVersion int) (Artifact, ArtifactVersion, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	v, err = prepareArtifactVersion(v)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.artifacts[artifactID]
	if !ok || a.TenantID != tid {
		return Artifact{}, ArtifactVersion{}, ErrArtifactNotFound
	}
	if baseVersion != a.CurrentVersion {
		return a, ArtifactVersion{}, ErrArtifactVersionConflict
	}
	v.ArtifactID = artifactID
	v.TenantID = tid
	v.Version = a.CurrentVersion + 1
	v.ParentVersion = a.CurrentVersion
	if v.CreatedAtMs <= 0 {
		v.CreatedAtMs = time.Now().UnixMilli()
	}
	if s.versions[artifactID] == nil {
		s.versions[artifactID] = map[int]ArtifactVersion{}
	}
	s.versions[artifactID][v.Version] = v
	a.CurrentVersion = v.Version
	a.UpdatedAtMs = v.CreatedAtMs
	if v.MimeType != "" {
		a.MimeType = v.MimeType
	}
	s.artifacts[artifactID] = a
	return s.withAppFactsLocked(a), v, nil
}

func (s *MemoryArtifacts) GetArtifactVersion(ctx context.Context, artifactID string, version int) (ArtifactVersion, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.artifacts[artifactID]
	if !ok || a.TenantID != tid {
		return ArtifactVersion{}, ErrArtifactNotFound
	}
	v, ok := s.versions[artifactID][version]
	if !ok {
		return ArtifactVersion{}, ErrArtifactNotFound
	}
	return v, nil
}

func (s *MemoryArtifacts) ListArtifactVersions(ctx context.Context, artifactID string, limit int) ([]ArtifactVersion, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	limit = artifactVersionLimit(limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ArtifactVersion{}
	a, ok := s.artifacts[artifactID]
	if !ok || a.TenantID != tid {
		return out, nil
	}
	for _, v := range s.versions[artifactID] {
		meta := v
		meta.Content = "" // metadata only: content is fetched per version
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryArtifacts) SetArtifactThumbnail(ctx context.Context, artifactID string, version int, t ArtifactThumbnailWrite) (ArtifactVersion, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactVersion{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.artifacts[artifactID]
	if !ok || a.TenantID != tid {
		return ArtifactVersion{}, false, ErrArtifactNotFound
	}
	v, ok := s.versions[artifactID][version]
	if !ok {
		return ArtifactVersion{}, false, ErrArtifactNotFound
	}
	if v.ThumbnailBlobKey != "" && v.ThumbnailRank >= t.Rank {
		return v, false, nil
	}
	v.ThumbnailBlobKey, v.ThumbnailRank = t.BlobKey, t.Rank
	v.Thumbnail = &ArtifactThumbnail{MimeType: t.MimeType, Width: t.Width, Height: t.Height}
	s.versions[artifactID][version] = v
	return v, true, nil
}

// ArtifactDownloadName is the file name a download of an artifact saves as:
// its file name, else its title. A site snapshot is a tar archive, so its
// name gets the .tar extension.
func ArtifactDownloadName(kind, fileName, title string) string {
	name := strings.TrimSpace(fileName)
	if name == "" {
		name = strings.TrimSpace(title)
	}
	if kind == ArtifactKindSite && name != "" && !strings.HasSuffix(strings.ToLower(name), ".tar") {
		name += ".tar"
	}
	return name
}
