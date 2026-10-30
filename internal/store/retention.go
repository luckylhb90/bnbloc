package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Data retention (docs/prd/2026-10-10-enterprise-scale.md §3). The policy is
// a platform default (platform setting retention.default) and a per-tenant
// override (tenants.retention_*_days). The retention worker (realtime)
// resolves the effective days per tenant and calls RetentionStore.Purge
// for each kind; afterwards a blob collection removes blobs no row
// references (ReferencedBlobKeys).

// Platform setting keys of retention.
const (
	// PlatformRetentionSettingKey holds the platform default
	// (wire.RetentionPolicy JSON; null kinds keep forever).
	PlatformRetentionSettingKey = "retention.default"
	// RetentionStatusSettingKey holds what the worker last did
	// (wire.RetentionStatus JSON).
	RetentionStatusSettingKey = "retention.status"
)

// LoadPlatformRetention reads the platform default (all null when unset).
func LoadPlatformRetention(ctx context.Context, st PlatformSettingsStore) (wire.RetentionPolicy, error) {
	row, ok, err := st.GetPlatformSetting(ctx, PlatformRetentionSettingKey)
	if err != nil || !ok {
		return wire.RetentionPolicy{}, err
	}
	var p wire.RetentionPolicy
	if err := json.Unmarshal([]byte(row.Value), &p); err != nil {
		return wire.RetentionPolicy{}, fmt.Errorf("retention setting is corrupt: %w", err)
	}
	return p.AsDefault(), nil
}

// SavePlatformRetention persists the platform default (normalized: 0 and
// null both keep forever and are stored as null).
func SavePlatformRetention(ctx context.Context, st PlatformSettingsStore, p wire.RetentionPolicy, actor string, atMs int64) error {
	raw, err := json.Marshal(p.AsDefault())
	if err != nil {
		return err
	}
	return st.PutPlatformSetting(ctx, PlatformSetting{Key: PlatformRetentionSettingKey, Value: string(raw), UpdatedAtMs: atMs, UpdatedBy: actor})
}

// LoadRetentionStatus reads the worker's last status (zero when it never ran).
func LoadRetentionStatus(ctx context.Context, st PlatformSettingsStore) (wire.RetentionStatus, error) {
	status := wire.RetentionStatus{LastDeleted: map[string]int64{}}
	row, ok, err := st.GetPlatformSetting(ctx, RetentionStatusSettingKey)
	if err != nil || !ok {
		return status, err
	}
	if err := json.Unmarshal([]byte(row.Value), &status); err != nil {
		return wire.RetentionStatus{LastDeleted: map[string]int64{}}, fmt.Errorf("retention status is corrupt: %w", err)
	}
	if status.LastDeleted == nil {
		status.LastDeleted = map[string]int64{}
	}
	return status, nil
}

// SaveRetentionStatus persists the worker's status.
func SaveRetentionStatus(ctx context.Context, st PlatformSettingsStore, status wire.RetentionStatus, atMs int64) error {
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return st.PutPlatformSetting(ctx, PlatformSetting{Key: RetentionStatusSettingKey, Value: string(raw), UpdatedAtMs: atMs, UpdatedBy: "retention"})
}

// Purged item names: what a purge deleted (or redacted), by table. They are
// the retention metric's `item` label and the status counters.
const (
	PurgedRoomMessages     = "room_messages"
	PurgedRoomMessagesKept = "room_messages_redacted"
	PurgedMessageTasks     = "message_tasks"
	PurgedAgentMessages    = "agent_messages"
	PurgedAttachments      = "attachments"
	PurgedArtifactVersions = "artifact_versions"
	PurgedArtifactShares   = "artifact_shares"
	PurgedBorrowAudit      = "shared_agent_audit"
	PurgedExtensionAudit   = "extension_audit"
	PurgedAgentJobs        = "agent_jobs"
	PurgedBlobs            = "blobs"
	PurgedContentDrafts    = "content_drafts"
	// PurgedSearchDocuments counts documents removed from an external
	// message search index (internal/search) after the message purge.
	PurgedSearchDocuments = "search_documents"
)

// RetentionFenceKind is the kind of a room message retention redacted
// instead of deleting, because it held the room's highest sequence.
//
// A room with no counter yet starts it at the durable max
// (ChatStore.AppendNextRoomMessages, ChatStore.MaxRoomSeq): deleting a
// room's newest row would lower that max and the room would re-issue
// sequences clients already saw (and that read cursors already passed).
// So retention never deletes a room's
// max-sequence row: it strips it to a fence — kind retention_fence, empty
// payload, no author, text, client id or edit stamp, soft-deleted — which
// keeps only (room, seq, channel, time). Every reader already skips
// soft-deleted rows; MaxRoomSeq still counts it. A fence that is no longer
// the max (the room moved on) is deleted by a later pass.
const RetentionFenceKind = "retention_fence"

// PurgeRequest asks for every row of Kind in TenantID older than BeforeMs to
// be deleted, in batches of Batch rows. TenantID "" addresses platform rows
// (only the audit kind has any).
type PurgeRequest struct {
	Kind     wire.RetentionKind
	TenantID string
	BeforeMs int64
	Batch    int
	// NowMs stamps fenced rows' deleted_at_ms.
	NowMs int64
}

func (r PurgeRequest) batch() int { return clampLimit(r.Batch, 500, 5000) }

// PurgeResult counts deleted rows by item (Purged* names).
type PurgeResult map[string]int64

// Add merges o into r.
func (r PurgeResult) Add(o PurgeResult) {
	for k, v := range o {
		r[k] += v
	}
}

// RetentionStore deletes expired rows and answers which blobs rows still
// reference. Implementations are safe for concurrent use; concurrent purges
// of the same rows (several replicas) are harmless.
type RetentionStore interface {
	// Purge deletes the expired rows of one kind in one tenant, with their
	// dependent rows, until none is left (or ctx ends). Each batch is its own
	// short statement/transaction.
	Purge(ctx context.Context, req PurgeRequest) (PurgeResult, error)
	// ReferencedBlobKeys returns the subset of keys some row still
	// references: a column of blobKeySources (listed once, read by both
	// implementations) or any "sha256/<hex>" a platform setting mentions
	// (component packages, desktop releases, the update config). A new
	// column that stores a blob key joins blobKeySources
	// (TestBlobKeyColumnsReferenced).
	ReferencedBlobKeys(ctx context.Context, keys []string) (map[string]bool, error)
	// PurgeExpiredDrafts deletes the content drafts expired at req.NowMs
	// (staged for a quick try, they expire instead of following a policy),
	// in batches of req.Batch.
	PurgeExpiredDrafts(ctx context.Context, req PurgeRequest) (PurgeResult, error)
	// TryRetentionLock takes the cluster-wide retention lock without
	// waiting; ok=false when another replica holds it. release frees it.
	TryRetentionLock(ctx context.Context) (release func(), ok bool, err error)
}

// blobKeySource is one column that stores a blob key: MySQL reads the
// column, the memory store reads the same values through memory.
type blobKeySource struct {
	table, column string
	memory        func(m *MemoryRetention, see func(key string))
}

// blobKeySources are every column holding a blob key a row still uses, in
// one place: MySQLRetention.ReferencedBlobKeys queries each (on its blob
// index) and MemoryRetention.ReferencedBlobKeys reads each, so the two can
// never list different sources. TestBlobKeyColumnsReferenced requires every
// *blob_key column of schema.sql here, and a contract case on both
// backends for each entry.
var blobKeySources = []blobKeySource{
	{"attachments", "blob_key", func(m *MemoryRetention, see func(string)) {
		if at := m.Attachments; at != nil {
			at.mu.Lock()
			defer at.mu.Unlock()
			for _, a := range at.m {
				see(a.BlobKey)
			}
		}
	}},
	{"artifact_versions", "blob_key", func(m *MemoryRetention, see func(string)) {
		m.eachArtifactVersion(func(v ArtifactVersion) { see(v.BlobKey) })
	}},
	{"artifact_versions", "thumb_blob_key", func(m *MemoryRetention, see func(string)) {
		m.eachArtifactVersion(func(v ArtifactVersion) { see(v.ThumbnailBlobKey) })
	}},
	{"content_component_versions", "bundle_blob_key", func(m *MemoryRetention, see func(string)) {
		if c := m.Content; c != nil {
			c.mu.Lock()
			defer c.mu.Unlock()
			for _, vs := range c.versions {
				for _, v := range vs {
					see(v.BundleBlobKey)
				}
			}
		}
	}},
	{"content_version_covers", "blob_key", func(m *MemoryRetention, see func(string)) {
		if c := m.Content; c != nil {
			c.mu.Lock()
			defer c.mu.Unlock()
			for _, cv := range c.covers {
				see(cv.BlobKey)
			}
		}
	}},
	{"app_files", "blob_key", func(m *MemoryRetention, see func(string)) {
		if a := m.AppDocs; a != nil {
			a.eachAppFile(func(f AppFile) { see(f.BlobKey) })
		}
	}},
	{"content_drafts", "bundle_blob_key", func(m *MemoryRetention, see func(string)) {
		if c := m.Content; c != nil {
			c.mu.Lock()
			defer c.mu.Unlock()
			for _, d := range c.drafts {
				see(d.BundleBlobKey)
			}
		}
	}},
}

// eachArtifactVersion visits every stored artifact version.
func (m *MemoryRetention) eachArtifactVersion(fn func(ArtifactVersion)) {
	a := m.Artifacts
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, vs := range a.versions {
		for _, v := range vs {
			fn(v)
		}
	}
}

// blobKeyInText finds blob keys mentioned in a platform setting value
// (artifact URLs end in /v1/artifacts/sha256/<hex>).
var blobKeyInText = regexp.MustCompile(digest.KeyPattern)

// platformBlobKeys collects every blob key the platform settings mention.
func platformBlobKeys(ctx context.Context, st PlatformSettingsStore) (map[string]bool, error) {
	out := map[string]bool{}
	if st == nil {
		return out, nil
	}
	rows, err := st.ListPlatformSettings(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		for _, k := range blobKeyInText.FindAllString(row.Value, -1) {
			out[k] = true
		}
	}
	return out, nil
}

// NewRetention returns the retention store over b's backends: MySQL when b
// has a DB, else the in-memory stores b holds.
func NewRetention(b *Backends) RetentionStore {
	if b.DB != nil {
		return NewMySQLRetention(b.DB)
	}
	m := &MemoryRetention{Settings: b.Settings}
	m.Chat, _ = b.Chat.(*MemoryChat)
	m.Tasks, _ = b.MessageTasks.(*MemoryMessageTasks)
	m.Activity, _ = b.Activity.(*Memory)
	m.Attachments, _ = b.Attachments.(*MemoryAttachments)
	m.AppDocs, _ = b.AppDocs.(*MemoryAppDocs)
	m.Artifacts, _ = b.Artifacts.(*MemoryArtifacts)
	m.Grants, _ = b.Grant.(*MemoryGrant)
	m.ExtAudit, _ = b.ExtAudit.(*MemoryExtensionAudit)
	m.Jobs, _ = b.BridgeJobs.(*MemoryBridgeJobs)
	m.Content, _ = b.ContentComponents.(*MemoryContentComponents)
	return m
}

// MemoryRetention is the RetentionStore over the in-memory stores (dev and
// tests). A nil store is skipped.
type MemoryRetention struct {
	Chat        *MemoryChat
	Tasks       *MemoryMessageTasks
	Activity    *Memory
	Attachments *MemoryAttachments
	Artifacts   *MemoryArtifacts
	Grants      *MemoryGrant
	ExtAudit    *MemoryExtensionAudit
	Jobs        *MemoryBridgeJobs
	Content     *MemoryContentComponents
	AppDocs     *MemoryAppDocs
	Settings    PlatformSettingsStore

	lock sync.Mutex
}

var _ RetentionStore = (*MemoryRetention)(nil)

// TryRetentionLock takes the process-local lock (one memory store is one
// process).
func (m *MemoryRetention) TryRetentionLock(context.Context) (func(), bool, error) {
	if !m.lock.TryLock() {
		return nil, false, nil
	}
	return m.lock.Unlock, true, nil
}

// Purge deletes the expired rows of one kind (the memory store has no batch
// cost, so it deletes them in one step).
func (m *MemoryRetention) Purge(ctx context.Context, req PurgeRequest) (PurgeResult, error) {
	out := PurgeResult{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	switch req.Kind {
	case wire.RetentionMessages:
		m.purgeMessages(req, out)
	case wire.RetentionArtifactVersions:
		m.purgeArtifactVersions(req, out)
	case wire.RetentionAudit:
		m.purgeAudit(req, out)
	case wire.RetentionAgentJobs:
		m.purgeJobs(req, out)
	default:
		return out, fmt.Errorf("retention: unknown kind %q", req.Kind)
	}
	return out, nil
}

func (m *MemoryRetention) purgeMessages(req PurgeRequest, out PurgeResult) {
	type roomSeq struct {
		room string
		seq  int64
	}
	var gone []roomSeq
	if c := m.Chat; c != nil {
		c.mu.Lock()
		prefix := req.TenantID + "\x00"
		for rk, msgs := range c.msgs {
			if !strings.HasPrefix(rk, prefix) {
				continue
			}
			room := strings.TrimPrefix(rk, prefix)
			var mx int64
			for _, msg := range msgs {
				mx = max(mx, msg.Seq)
			}
			kept := msgs[:0]
			for _, msg := range msgs {
				dk := rk + "\x00" + strconv.FormatInt(msg.Seq, 10)
				switch {
				case msg.Kind == RetentionFenceKind && msg.Seq < mx:
					// A fence the room moved past.
					delete(c.deleted, dk)
					out[PurgedRoomMessages]++
				case msg.Kind == RetentionFenceKind || msg.CreatedAtMs >= req.BeforeMs:
					kept = append(kept, msg)
				case msg.Seq == mx:
					// The room's max-seq row is redacted, never deleted.
					if msg.ClientMsgID != "" {
						delete(c.byCMI, rk+"\x00"+msg.ClientMsgID)
					}
					msg.Kind, msg.Author, msg.ClientMsgID, msg.Payload, msg.EditedAtMs = RetentionFenceKind, "", "", json.RawMessage("{}"), nil
					c.deleted[dk] = true
					kept = append(kept, msg)
					gone = append(gone, roomSeq{room, msg.Seq})
					out[PurgedRoomMessagesKept]++
				default:
					if msg.ClientMsgID != "" {
						delete(c.byCMI, rk+"\x00"+msg.ClientMsgID)
					}
					delete(c.deleted, dk)
					gone = append(gone, roomSeq{room, msg.Seq})
					out[PurgedRoomMessages]++
				}
			}
			c.msgs[rk] = kept
		}
		c.mu.Unlock()
	}
	if t := m.Tasks; t != nil && len(gone) > 0 {
		t.mu.Lock()
		for _, g := range gone {
			rows := t.rows[req.TenantID+"\x00"+g.room]
			if _, ok := rows[g.seq]; ok {
				delete(rows, g.seq)
				out[PurgedMessageTasks]++
			}
		}
		t.mu.Unlock()
	}
	if a := m.Artifacts; a != nil && len(gone) > 0 {
		a.mu.Lock()
		bySeq := map[roomSeq]bool{}
		for _, g := range gone {
			bySeq[g] = true
		}
		for id, art := range a.artifacts {
			if art.TenantID != req.TenantID {
				continue
			}
			for n, v := range a.versions[id] {
				if v.SourceSeq > 0 && bySeq[roomSeq{art.RoomID, v.SourceSeq}] {
					v.SourceSeq = 0
					a.versions[id][n] = v
				}
			}
		}
		a.mu.Unlock()
	}
	if act := m.Activity; act != nil {
		act.mu.Lock()
		kept := act.usage[:0]
		for _, u := range act.usage {
			if u.tenant == req.TenantID && u.createdAtMs > 0 && u.createdAtMs < req.BeforeMs {
				out[PurgedAgentMessages]++
				continue
			}
			kept = append(kept, u)
		}
		act.usage = kept
		act.mu.Unlock()
	}
	if at := m.Attachments; at != nil {
		at.mu.Lock()
		prefix := req.TenantID + "\x00"
		for k, a := range at.m {
			if strings.HasPrefix(k, prefix) && a.CreatedAtMs < req.BeforeMs {
				delete(at.m, k)
				out[PurgedAttachments]++
			}
		}
		at.mu.Unlock()
	}
}

func (m *MemoryRetention) purgeArtifactVersions(req PurgeRequest, out PurgeResult) {
	a := m.Artifacts
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	type pin struct {
		artifact string
		version  int
	}
	gone := map[pin]bool{}
	for id, art := range a.artifacts {
		if art.TenantID != req.TenantID {
			continue
		}
		for n, v := range a.versions[id] {
			if n < art.CurrentVersion && v.CreatedAtMs < req.BeforeMs {
				delete(a.versions[id], n)
				gone[pin{id, n}] = true
				out[PurgedArtifactVersions]++
			}
		}
	}
	for id, sh := range a.shares {
		if sh.TenantID == req.TenantID && sh.Version != 0 && gone[pin{sh.ArtifactID, sh.Version}] {
			delete(a.shares, id)
			out[PurgedArtifactShares]++
		}
	}
}

func (m *MemoryRetention) purgeAudit(req PurgeRequest, out PurgeResult) {
	if g := m.Grants; g != nil {
		g.mu.Lock()
		kept := g.audit[:0]
		for _, r := range g.audit {
			if r.TenantID == req.TenantID && r.AtMs < req.BeforeMs {
				out[PurgedBorrowAudit]++
				continue
			}
			kept = append(kept, r)
		}
		g.audit = kept
		g.mu.Unlock()
	}
	if e := m.ExtAudit; e != nil {
		e.mu.Lock()
		kept := e.recs[:0]
		for _, r := range e.recs {
			if r.TenantID == req.TenantID && r.AtMs < req.BeforeMs {
				out[PurgedExtensionAudit]++
				continue
			}
			kept = append(kept, r)
		}
		e.recs = kept
		e.mu.Unlock()
	}
}

func (m *MemoryRetention) purgeJobs(req PurgeRequest, out PurgeResult) {
	j := m.Jobs
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for id, job := range j.jobs {
		if job.TenantID != req.TenantID || !job.Status.Terminal() || job.CreatedAtMs >= req.BeforeMs || job.FinishedAtMs >= req.BeforeMs {
			continue
		}
		delete(j.jobs, id)
		for origin, jobID := range j.origin {
			if jobID == id {
				delete(j.origin, origin)
			}
		}
		out[PurgedAgentJobs]++
	}
}

// ReferencedBlobKeys answers from blobKeySources over the memory stores and
// the platform settings (see RetentionStore).
func (m *MemoryRetention) ReferencedBlobKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	platform, err := platformBlobKeys(ctx, m.Settings)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(keys))
	out := map[string]bool{}
	for _, k := range keys {
		want[k] = true
		if platform[strings.ToLower(k)] {
			out[k] = true
		}
	}
	for _, src := range blobKeySources {
		src.memory(m, func(key string) {
			if want[key] {
				out[key] = true
			}
		})
	}
	return out, nil
}

// PurgeExpiredDrafts deletes expired content drafts (see RetentionStore).
func (m *MemoryRetention) PurgeExpiredDrafts(ctx context.Context, req PurgeRequest) (PurgeResult, error) {
	out := PurgeResult{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if c := m.Content; c != nil {
		if n := c.purgeExpiredDrafts(req.NowMs); n > 0 {
			out[PurgedContentDrafts] = n
		}
	}
	return out, nil
}

// sortedSet returns m's keys in order (stable SQL argument lists).
func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
