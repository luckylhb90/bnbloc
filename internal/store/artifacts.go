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

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

var ErrArtifactNotFound = errors.New("store: artifact not found")
var ErrArtifactVersionConflict = errors.New("store: artifact version conflict")

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
}

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
// paging on (UpdatedAtMs, ArtifactID).
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
	// Text substring-matches the title or file name (case-insensitive).
	Text string
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
	PutArtifact(ctx context.Context, a Artifact) (Artifact, error)
	GetArtifact(ctx context.Context, artifactID string) (Artifact, error)
	ListArtifacts(ctx context.Context, q ArtifactQuery) ([]Artifact, error)
	// PublishArtifactVersion appends revision baseVersion+1 (compare-and-swap
	// on the current version). An inline version's hash and size are computed
	// from Content; a blob version carries its blob's ContentHash, BlobKey and
	// SizeBytes. A non-empty MimeType becomes the artifact's current MIME type.
	PublishArtifactVersion(ctx context.Context, artifactID string, v ArtifactVersion, baseVersion int) (Artifact, ArtifactVersion, error)
	GetArtifactVersion(ctx context.Context, artifactID string, version int) (ArtifactVersion, error)
	ListArtifactVersions(ctx context.Context, artifactID string, limit int) []ArtifactVersion
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
		if v.BlobKey == "" || len(v.ContentHash) != 64 || v.Content != "" || v.SizeBytes < 0 {
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

func artifactListLimit(n int) int {
	if n <= 0 || n > 200 {
		return 100
	}
	return n
}

// MemoryArtifacts is the in-memory ArtifactStore (dev / tests).
type MemoryArtifacts struct {
	mu        sync.Mutex
	artifacts map[string]Artifact
	versions  map[string]map[int]ArtifactVersion
	shares    map[string]ArtifactShare
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
		// Idempotent attach: a re-put with the same id keeps the artifact's
		// version history instead of resetting it.
		return existing, nil
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
	return a, nil
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
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAtMs != out[j].UpdatedAtMs {
			return out[i].UpdatedAtMs > out[j].UpdatedAtMs
		}
		return out[i].ArtifactID > out[j].ArtifactID
	})
	if len(out) > limit {
		out = out[:limit]
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
	return a, v, nil
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

func (s *MemoryArtifacts) ListArtifactVersions(ctx context.Context, artifactID string, limit int) []ArtifactVersion {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.artifacts[artifactID]
	if !ok || a.TenantID != tid {
		return nil
	}
	var out []ArtifactVersion
	for _, v := range s.versions[artifactID] {
		meta := v
		meta.Content = "" // metadata only: content is fetched per version
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
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
