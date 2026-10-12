package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// retentionRig is one backend set (memory, and MySQL when
// BNBLOC_TEST_MYSQL_DSN is set) with its retention store.
type retentionRig struct {
	name string
	b    *Backends
	r    RetentionStore
}

func retentionRigs(t *testing.T) []retentionRig {
	t.Helper()
	mem, err := NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	rigs := []retentionRig{{"memory", mem, NewRetention(mem)}}
	if dsn := sharedMySQLDSN(t); dsn != "" {
		b, err := NewBackends(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { b.DB.Close() })
		rigs = append(rigs, retentionRig{"mysql", b, NewRetention(b)})
	}
	return rigs
}

// uniqSeq makes uniq unique within a run: two calls in the same clock tick
// returned the same id, so two messages shared a client message id.
var uniqSeq atomic.Int64

// uniq returns an id unique across runs (the clock) and within one (uniqSeq),
// so tests sharing a MySQL database never collide.
func uniq(prefix string) string {
	return prefix + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(uniqSeq.Add(1), 36)
}

const day = int64(24 * time.Hour / time.Millisecond)

func mustAppend(t *testing.T, ctx context.Context, b *Backends, room string, seq, at int64, text string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"text": text, "userId": "maya"})
	if err := b.Chat.AppendRoomMessage(ctx, RoomMessage{RoomID: room, Seq: seq, Kind: "chat_message", Author: "maya",
		ClientMsgID: uniq("c"), Payload: payload, CreatedAtMs: at}); err != nil {
		t.Fatal(err)
	}
}

func historySeqs(t *testing.T, ctx context.Context, b *Backends, room string) []int64 {
	t.Helper()
	msgs, err := b.Chat.RoomHistory(ctx, room, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, m := range msgs {
		out = append(out, m.Seq)
	}
	return out
}

func eqSeqs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Messages: rows older than the cutoff are gone with their tasks, artifact
// source links, activity copies and attachments; newer rows and other
// tenants are untouched; a room's max-seq row is redacted (never deleted) so
// MaxRoomSeq never goes down, and the fence is deleted once the room moves
// past it.
func TestRetentionPurgeMessages(t *testing.T) {
	for _, rig := range retentionRigs(t) {
		t.Run(rig.name, func(t *testing.T) {
			tid, other := uniq("acme-"), uniq("other-")
			ctx, octx := tenantCtx(tid), tenantCtx(other)
			b := rig.b
			now := time.Now().UnixMilli()
			old, cutoff := now-60*day, now-31*day
			busy, quiet := uniq("busy-"), uniq("quiet-")
			mustAppend(t, ctx, b, busy, 1, old, "old one")
			mustAppend(t, ctx, b, busy, 2, old+1, "old two")
			mustAppend(t, ctx, b, busy, 3, now, "new")
			mustAppend(t, ctx, b, quiet, 1, old, "quiet one")
			mustAppend(t, ctx, b, quiet, 2, old+1, "quiet newest")
			elsewhere := uniq("elsewhere-")
			mustAppend(t, octx, b, elsewhere, 1, old, "other tenant")
			for _, seq := range []int64{1, 3} {
				if _, err := b.MessageTasks.PutMessageTask(ctx, wire.MessageTask{RoomID: busy, Seq: seq, Status: "open", Title: "t", CreatedBy: "maya", CreatedAtMs: old}); err != nil {
					t.Fatal(err)
				}
			}
			art, err := b.Artifacts.PutArtifact(ctx, Artifact{ArtifactID: uniq("a"), RoomID: busy, Kind: "code", Title: "x", CreatedBy: "maya", CreatedAtMs: now, UpdatedAtMs: now})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := b.Artifacts.PublishArtifactVersion(ctx, art.ArtifactID, ArtifactVersion{Content: "v1", AuthorUserID: "maya", SourceSeq: 1, CreatedAtMs: now}, 0); err != nil {
				t.Fatal(err)
			}
			for i, at := range []int64{old, now} {
				if err := b.Attachments.PutAttachment(ctx, Attachment{ID: uniq("att" + strconv.Itoa(i)), RoomID: busy, Uploader: "maya", Filename: "f.txt", ContentType: "text/plain", Size: 1, SHA256: "x", BlobKey: "sha256/x", CreatedAtMs: at}); err != nil {
					t.Fatal(err)
				}
			}
			reply := func(seq, at int64) wire.Message {
				return wire.Message{Seq: seq, CreatedAtUnixMs: at, Payload: map[string]any{"ownerUserId": "maya", "provider": "codex", "metrics": map[string]any{"inputTokens": 10, "outputTokens": 5}}}
			}
			if _, _, err := b.Activity.AppendMessages(ctx, busy, wire.ReportMessagesInput{WorkspaceID: busy, AgentSessionID: "s1", Messages: []wire.Message{reply(1, old), reply(2, now)}}); err != nil {
				t.Fatal(err)
			}

			res, err := rig.r.Purge(ctx, PurgeRequest{Kind: wire.RetentionMessages, TenantID: tid, BeforeMs: cutoff, Batch: 2, NowMs: now})
			if err != nil {
				t.Fatal(err)
			}
			if res[PurgedRoomMessages] != 3 || res[PurgedRoomMessagesKept] != 1 || res[PurgedMessageTasks] != 1 || res[PurgedAttachments] != 1 || res[PurgedAgentMessages] != 1 {
				t.Fatalf("purge result = %v", res)
			}
			if got := historySeqs(t, ctx, b, busy); !eqSeqs(got, []int64{3}) {
				t.Fatalf("busy history = %v", got)
			}
			if got := historySeqs(t, ctx, b, quiet); len(got) != 0 {
				t.Fatalf("quiet history = %v (the fence must not show)", got)
			}
			for room, want := range map[string]int64{busy: 3, quiet: 2} {
				if got, err := b.Chat.MaxRoomSeq(ctx, room, ""); err != nil || got != want {
					t.Fatalf("MaxRoomSeq(%s) = %d, %v; want %d", room, got, err, want)
				}
			}
			if _, found, _ := b.MessageTasks.GetMessageTask(ctx, busy, 1); found {
				t.Fatal("the task of a deleted message survived")
			}
			if _, found, _ := b.MessageTasks.GetMessageTask(ctx, busy, 3); !found {
				t.Fatal("the task of a kept message was deleted")
			}
			if v, err := b.Artifacts.GetArtifactVersion(ctx, art.ArtifactID, 1); err != nil || v.SourceSeq != 0 {
				t.Fatalf("artifact source link = %+v, %v (want cleared)", v, err)
			}
			if _, found, _ := b.Chat.GetRoomMessage(ctx, quiet, 2); found {
				t.Fatal("the fence must read as deleted")
			}
			turns := 0
			for _, u := range b.AgentUsage().AgentUsage(ctx, "", 0) {
				turns += u.ReportedTurns
			}
			if turns != 1 {
				t.Fatalf("agent replies left = %d, want 1", turns)
			}
			if got := historySeqs(t, octx, b, elsewhere); !eqSeqs(got, []int64{1}) {
				t.Fatalf("other tenant history = %v", got)
			}

			// A second pass finds nothing; once the quiet room moves on, its
			// fence is deleted and the max follows the new row.
			res, err = rig.r.Purge(ctx, PurgeRequest{Kind: wire.RetentionMessages, TenantID: tid, BeforeMs: cutoff, NowMs: now})
			if err != nil || res[PurgedRoomMessages] != 0 || res[PurgedRoomMessagesKept] != 0 {
				t.Fatalf("second pass = %v, %v", res, err)
			}
			mustAppend(t, ctx, b, quiet, 3, now, "back")
			res, err = rig.r.Purge(ctx, PurgeRequest{Kind: wire.RetentionMessages, TenantID: tid, BeforeMs: cutoff, NowMs: now})
			if err != nil || res[PurgedRoomMessages] != 1 {
				t.Fatalf("fence cleanup = %v, %v", res, err)
			}
			if got, _ := b.Chat.MaxRoomSeq(ctx, quiet, ""); got != 3 {
				t.Fatalf("MaxRoomSeq after fence cleanup = %d", got)
			}
			if got := historySeqs(t, ctx, b, quiet); !eqSeqs(got, []int64{3}) {
				t.Fatalf("quiet history after cleanup = %v", got)
			}
		})
	}
}

// Artifact versions: only non-latest versions older than the cutoff go,
// with the share links pinned to them; the latest version stays however
// old it is.
func TestRetentionPurgeArtifactVersions(t *testing.T) {
	for _, rig := range retentionRigs(t) {
		t.Run(rig.name, func(t *testing.T) {
			tid := uniq("acme-")
			ctx := tenantCtx(tid)
			b := rig.b
			now := time.Now().UnixMilli()
			old, cutoff := now-90*day, now-31*day
			room := uniq("room-")
			stale, err := b.Artifacts.PutArtifact(ctx, Artifact{ArtifactID: uniq("a"), RoomID: room, Kind: "code", Title: "stale", CreatedBy: "maya", CreatedAtMs: old, UpdatedAtMs: old})
			if err != nil {
				t.Fatal(err)
			}
			for i, at := range []int64{old, old + 1, now} {
				if _, _, err := b.Artifacts.PublishArtifactVersion(ctx, stale.ArtifactID, ArtifactVersion{Content: "v" + strconv.Itoa(i), AuthorUserID: "maya", CreatedAtMs: at}, i); err != nil {
					t.Fatal(err)
				}
			}
			frozen, err := b.Artifacts.PutArtifact(ctx, Artifact{ArtifactID: uniq("b"), RoomID: room, Kind: "code", Title: "frozen", CreatedBy: "maya", CreatedAtMs: old, UpdatedAtMs: old})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := b.Artifacts.PublishArtifactVersion(ctx, frozen.ArtifactID, ArtifactVersion{Content: "only", AuthorUserID: "maya", CreatedAtMs: old}, 0); err != nil {
				t.Fatal(err)
			}
			shares := b.Artifacts.(ArtifactShareStore)
			for i, v := range []int{1, 0} {
				if _, err := shares.CreateArtifactShare(ctx, ArtifactShare{ShareID: uniq("s" + strconv.Itoa(i)), TokenHash: fmt.Sprintf("%064x", time.Now().UnixNano()+int64(i)), RoomID: room, ArtifactID: stale.ArtifactID, Version: v, CreatedBy: "maya", CreatedAtMs: now, ExpiresAtMs: now + day}); err != nil {
					t.Fatal(err)
				}
			}
			res, err := rig.r.Purge(ctx, PurgeRequest{Kind: wire.RetentionArtifactVersions, TenantID: tid, BeforeMs: cutoff, Batch: 1})
			if err != nil {
				t.Fatal(err)
			}
			if res[PurgedArtifactVersions] != 2 || res[PurgedArtifactShares] != 1 {
				t.Fatalf("purge = %v", res)
			}
			if got := b.Artifacts.ListArtifactVersions(ctx, stale.ArtifactID, 10); len(got) != 1 || got[0].Version != 3 {
				t.Fatalf("stale versions = %+v", got)
			}
			if got := b.Artifacts.ListArtifactVersions(ctx, frozen.ArtifactID, 10); len(got) != 1 {
				t.Fatalf("the latest version must stay: %+v", got)
			}
			left, err := shares.ListArtifactShares(ctx, room, stale.ArtifactID, 0)
			if err != nil || len(left) != 1 || left[0].Version != 0 {
				t.Fatalf("shares = %+v, %v", left, err)
			}
		})
	}
}

// Audit and finished agent jobs: old rows go, newer rows, unfinished jobs
// and platform rows (tenant "") of another scope stay.
func TestRetentionPurgeAuditAndJobs(t *testing.T) {
	for _, rig := range retentionRigs(t) {
		t.Run(rig.name, func(t *testing.T) {
			tid := uniq("acme-")
			ctx := tenantCtx(tid)
			b := rig.b
			now := time.Now().UnixMilli()
			old, cutoff := now-400*day, now-365*day
			room := uniq("room-")
			for _, at := range []int64{old, now} {
				if err := b.Grant.AppendAudit(ctx, AuditRecord{RoomID: room, ActorUserID: "jack", OwnerUserID: "maya", Action: "turn", Decision: "allow", AtMs: at}); err != nil {
					t.Fatal(err)
				}
				if err := b.ExtAudit.AppendExtAudit(ctx, ExtAuditRecord{Kind: "member", Name: "maya", Action: "role", TenantID: tid, AtMs: at}); err != nil {
					t.Fatal(err)
				}
			}
			platformName := uniq("platform-")
			if err := b.ExtAudit.AppendExtAudit(ctx, ExtAuditRecord{Kind: "tenant", Name: platformName, Action: "create", AtMs: old}); err != nil {
				t.Fatal(err)
			}
			jobs := []AgentJob{
				{JobID: uniq("j1"), MessageSeq: 1, Status: AgentJobReplied, CreatedAtMs: old, FinishedAtMs: old + 1},
				{JobID: uniq("j2"), MessageSeq: 2, Status: AgentJobQueued, CreatedAtMs: old},
				{JobID: uniq("j3"), MessageSeq: 3, Status: AgentJobFailed, CreatedAtMs: now, FinishedAtMs: now},
			}
			for _, j := range jobs {
				j.RoomID, j.RequesterUserID, j.OwnerUserID, j.Core, j.Provider = room, "jack", "maya", "acme", "codex"
				if _, err := b.BridgeJobs.PutAgentJob(ctx, j); err != nil {
					t.Fatal(err)
				}
			}
			res, err := rig.r.Purge(ctx, PurgeRequest{Kind: wire.RetentionAudit, TenantID: tid, BeforeMs: cutoff, Batch: 1})
			if err != nil || res[PurgedBorrowAudit] != 1 || res[PurgedExtensionAudit] != 1 {
				t.Fatalf("audit purge = %v, %v", res, err)
			}
			rows, _, err := MergeAuditLog(ctx, AuditLogQuery{TenantID: tid, Limit: 50}, b.ExtAudit.(AuditLogReader), b.Grant.(AuditLogReader))
			if err != nil || len(rows) != 2 {
				t.Fatalf("audit left = %+v, %v", rows, err)
			}
			platform, _, err := MergeAuditLog(ctx, AuditLogQuery{AllTenants: true, Kinds: []string{"tenant"}, Limit: 500}, b.ExtAudit.(AuditLogReader))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, r := range platform {
				found = found || r.Name == platformName
			}
			if !found {
				t.Fatal("a tenant purge must not touch platform rows")
			}
			res, err = rig.r.Purge(ctx, PurgeRequest{Kind: wire.RetentionAgentJobs, TenantID: tid, BeforeMs: cutoff})
			if err != nil || res[PurgedAgentJobs] != 1 {
				t.Fatalf("jobs purge = %v, %v", res, err)
			}
			left := b.BridgeJobs.ListAgentJobs(ctx, room, "", 10)
			if len(left) != 2 {
				t.Fatalf("jobs left = %+v", left)
			}
			for _, j := range left {
				if j.JobID == jobs[0].JobID {
					t.Fatal("the old finished job survived")
				}
			}
		})
	}
}

// Blob references come from attachments, artifact versions (bytes and
// thumbnails) and any key a platform setting mentions.
func TestRetentionReferencedBlobKeys(t *testing.T) {
	for _, rig := range retentionRigs(t) {
		t.Run(rig.name, func(t *testing.T) {
			ctx := tenantCtx(uniq("acme-"))
			b := rig.b
			key := func(c string) string {
				return "sha256/" + strings.Repeat(c, 63) + strconv.FormatInt(time.Now().UnixNano()%10, 10)
			}
			att, ver, thumb, setting, free := key("a"), key("b"), key("c"), key("d"), key("e")
			room := uniq("room-")
			if err := b.Attachments.PutAttachment(ctx, Attachment{ID: uniq("att"), RoomID: room, Uploader: "maya", Filename: "f", ContentType: "text/plain", BlobKey: att, CreatedAtMs: 1}); err != nil {
				t.Fatal(err)
			}
			art, err := b.Artifacts.PutArtifact(ctx, Artifact{ArtifactID: uniq("a"), RoomID: room, Kind: "file", Title: "f", CreatedBy: "maya", CreatedAtMs: 1, UpdatedAtMs: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := b.Artifacts.PublishArtifactVersion(ctx, art.ArtifactID, ArtifactVersion{Storage: ArtifactStorageBlob, BlobKey: ver, ContentHash: strings.TrimPrefix(ver, "sha256/"), SizeBytes: 1, MimeType: "application/pdf", AuthorUserID: "maya"}, 0); err != nil {
				t.Fatal(err)
			}
			if _, _, err := b.Artifacts.SetArtifactThumbnail(ctx, art.ArtifactID, 1, ArtifactThumbnailWrite{BlobKey: thumb, MimeType: "image/png", Width: 1, Height: 1, Rank: 1}); err != nil {
				t.Fatal(err)
			}
			if err := b.Settings.PutPlatformSetting(ctx, PlatformSetting{Key: uniq("ext.test."), Value: `{"url":"https://gw.example/v1/artifacts/` + setting + `"}`, UpdatedAtMs: 1, UpdatedBy: "maya"}); err != nil {
				t.Fatal(err)
			}
			got, err := rig.r.ReferencedBlobKeys(ctx, []string{att, ver, thumb, setting, free})
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{att, ver, thumb, setting} {
				if !got[k] {
					t.Fatalf("%s must be referenced: %v", k, got)
				}
			}
			if got[free] {
				t.Fatal("an unreferenced key was reported referenced")
			}
		})
	}
}

// One replica holds the retention lock at a time.
func TestRetentionLock(t *testing.T) {
	for _, rig := range retentionRigs(t) {
		t.Run(rig.name, func(t *testing.T) {
			ctx := context.Background()
			release, ok, err := rig.r.TryRetentionLock(ctx)
			if err != nil || !ok {
				t.Fatalf("first lock: %v %v", ok, err)
			}
			other := rig.r
			if rig.b.DB != nil {
				other = NewMySQLRetention(rig.b.DB)
			}
			if _, ok, err := other.TryRetentionLock(ctx); err != nil || ok {
				t.Fatalf("second lock while held: %v %v", ok, err)
			}
			release()
			release2, ok, err := other.TryRetentionLock(ctx)
			if err != nil || !ok {
				t.Fatalf("lock after release: %v %v", ok, err)
			}
			release2()
		})
	}
}

// The tenant override round-trips (null = platform default, 0 = keep
// forever) and survives an unrelated tenant upsert.
func TestTenantRetentionOverride(t *testing.T) {
	for _, rig := range retentionRigs(t) {
		t.Run(rig.name, func(t *testing.T) {
			ctx := context.Background()
			id := uniq("ret-")
			s := rig.b.Tenant
			if err := s.PutTenant(ctx, Tenant{TenantID: id, Name: "Acme", CreatedAtMs: 1}); err != nil {
				t.Fatal(err)
			}
			zero, ninety := 0, 90
			got, err := s.SetTenantRetention(ctx, id, wire.RetentionPolicy{MessagesDays: &ninety, AuditDays: &zero})
			if err != nil || got.Retention.MessagesDays == nil || *got.Retention.MessagesDays != 90 || got.Retention.AuditDays == nil || *got.Retention.AuditDays != 0 || got.Retention.AgentJobsDays != nil {
				t.Fatalf("set = %+v, %v", got.Retention, err)
			}
			if err := s.PutTenant(ctx, Tenant{TenantID: id, Name: "Renamed", CreatedAtMs: 1}); err != nil {
				t.Fatal(err)
			}
			got, err = s.GetTenant(ctx, id)
			if err != nil || got.Retention.MessagesDays == nil || *got.Retention.MessagesDays != 90 {
				t.Fatalf("after upsert = %+v, %v", got.Retention, err)
			}
			if got, err = s.SetTenantRetention(ctx, id, wire.RetentionPolicy{}); err != nil || got.Retention.MessagesDays != nil || got.Retention.AuditDays != nil {
				t.Fatalf("clear = %+v, %v", got.Retention, err)
			}
			if _, err := s.SetTenantRetention(ctx, "missing-"+id, wire.RetentionPolicy{}); err != ErrTenantNotFound {
				t.Fatalf("missing tenant: %v", err)
			}
		})
	}
}
