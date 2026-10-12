package retention

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/testmysql"

	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const day = 24 * time.Hour

func days(n int) *int { return &n }

type rig struct {
	b       *store.Backends
	w       *Worker
	now     time.Time
	dir     string
	metrics *obs.Metrics
}

func newRig(t *testing.T) *rig {
	t.Helper()
	b, err := store.NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	blobs, err := blobstore.NewDiskStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{b: b, now: time.Now(), dir: dir, metrics: obs.NewMetrics("test")}
	RegisterMetrics(r.metrics)
	r.w = &Worker{Store: store.NewRetention(b), Tenants: b.Tenant, Settings: b.Settings, Audit: b.ExtAudit, Blobs: blobs, Metrics: r.metrics,
		Now: func() time.Time { return r.now }}
	for _, id := range []string{"acme", "globex"} {
		if err := b.Tenant.PutTenant(context.Background(), store.Tenant{TenantID: id, Name: id, CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r *rig) msg(t *testing.T, tid, room string, seq int64, age time.Duration) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"text": "hello", "userId": "maya"})
	if err := r.b.Chat.AppendRoomMessage(tenant.NewContext(context.Background(), tid), store.RoomMessage{
		RoomID: room, Seq: seq, Kind: "chat_message", Author: "maya", Payload: payload, CreatedAtMs: r.now.Add(-age).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) history(t *testing.T, tid, room string) []int64 {
	t.Helper()
	msgs, err := r.b.Chat.RoomHistory(tenant.NewContext(context.Background(), tid), room, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, m := range msgs {
		out = append(out, m.Seq)
	}
	return out
}

// The worker resolves each tenant's effective days (override over the
// platform default; 0 keeps forever), deletes what expired, keeps newer
// rows, never lowers a room's max sequence, and records its status.
func TestWorkerAppliesEffectivePolicy(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := store.SavePlatformRetention(ctx, r.b.Settings, wire.RetentionPolicy{MessagesDays: days(365), AuditDays: days(400)}, "root", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.b.Tenant.SetTenantRetention(ctx, "acme", wire.RetentionPolicy{MessagesDays: days(31), AuditDays: days(0)}); err != nil {
		t.Fatal(err)
	}
	// acme: 31 days. globex: the 365-day platform default.
	r.msg(t, "acme", "acme-room", 1, 60*day)
	r.msg(t, "acme", "acme-room", 2, 40*day)
	r.msg(t, "acme", "acme-room", 3, time.Hour)
	r.msg(t, "acme", "acme-quiet", 1, 90*day)
	r.msg(t, "globex", "globex-room", 1, 60*day)
	r.msg(t, "globex", "globex-room", 2, 400*day)
	actx := tenant.NewContext(ctx, "acme")
	for _, at := range []time.Duration{500 * day, time.Hour} {
		if err := r.b.ExtAudit.AppendExtAudit(ctx, store.ExtAuditRecord{Kind: "member", Name: "maya", Action: "role", TenantID: "acme", AtMs: r.now.Add(-at).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		if err := r.b.ExtAudit.AppendExtAudit(ctx, store.ExtAuditRecord{Kind: "tenant", Name: "acme", Action: "create", AtMs: r.now.Add(-at).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := r.w.RunOnce(ctx)
	if err != nil || !rep.Ran {
		t.Fatalf("RunOnce = %+v, %v", rep, err)
	}
	if got := r.history(t, "acme", "acme-room"); len(got) != 1 || got[0] != 3 {
		t.Fatalf("acme history = %v", got)
	}
	if got := r.history(t, "acme", "acme-quiet"); len(got) != 0 {
		t.Fatalf("acme quiet history = %v", got)
	}
	if mx, _ := r.b.Chat.MaxRoomSeq(actx, "acme-quiet", ""); mx != 1 {
		t.Fatalf("the quiet room's max sequence went down to %d", mx)
	}
	// globex keeps 60-day-old messages under the 365-day default; its
	// 400-day-old row holds the room's max sequence, so it is redacted.
	if got := r.history(t, "globex", "globex-room"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("globex history = %v", got)
	}
	// acme keeps its audit forever (0); the platform rows follow the
	// 400-day default.
	recs, _ := r.b.ExtAudit.ExtAuditRecords(ctx, "", 100, 0)
	var acmeRows, platformRows int
	purgeRecord := ""
	for _, rec := range recs {
		switch {
		case rec.Kind == wire.AuditKindRetention:
			if rec.TenantID == "acme" && rec.Name == "messages" {
				purgeRecord = rec.Detail
			}
		case rec.TenantID == "acme":
			acmeRows++
		default:
			platformRows++
		}
	}
	if acmeRows != 2 || platformRows != 1 {
		t.Fatalf("audit rows acme=%d platform=%d", acmeRows, platformRows)
	}
	// The deletion itself is on record in the tenant's audit log.
	if !strings.Contains(purgeRecord, "older than 31 days") || !strings.Contains(purgeRecord, "room_messages=2") {
		t.Fatalf("purge audit record = %q", purgeRecord)
	}
	if rep.Deleted[store.PurgedRoomMessages] != 2 || rep.Deleted[store.PurgedRoomMessagesKept] != 2 || rep.Deleted[store.PurgedExtensionAudit] != 1 {
		t.Fatalf("report = %v", rep.Deleted)
	}
	status, err := store.LoadRetentionStatus(ctx, r.b.Settings)
	if err != nil || status.LastRunAtMs == 0 || status.LastBlobGCAtMs == 0 || status.LastDeleted[store.PurgedRoomMessages] != 2 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	rec := httptest.NewRecorder()
	r.metrics.Handler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `bnbloc_retention_deleted_total{service="test",item="room_messages"} 2`) {
		t.Fatalf("metrics missing the deleted count:\n%s", rec.Body.String())
	}
}

// A replica that cannot take the lock skips the pass.
func TestWorkerSkipsWhileAnotherReplicaHoldsTheLock(t *testing.T) {
	r := newRig(t)
	release, ok, err := r.w.Store.TryRetentionLock(context.Background())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer release()
	rep, err := r.w.RunOnce(context.Background())
	if err != nil || rep.Ran {
		t.Fatalf("RunOnce under a held lock = %+v, %v", rep, err)
	}
}

func (r *rig) blob(t *testing.T, content string, age time.Duration) string {
	t.Helper()
	key, _, sha, err := r.w.Blobs.Put(context.Background(), strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	at := r.now.Add(-age)
	if err := os.Chtimes(filepath.Join(r.dir, sha[:2], sha), at, at); err != nil {
		t.Fatal(err)
	}
	return key
}

func (r *rig) exists(key string) bool {
	_, err := r.w.Blobs.Stat(context.Background(), key)
	return err == nil
}

// The blob collection deletes old unreferenced blobs only: a blob shared by
// two rows survives the deletion of one of them, a blob inside the grace
// period survives unreferenced, and platform settings references count.
func TestBlobCollection(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	actx := tenant.NewContext(ctx, "acme")
	shared := r.blob(t, "shared bytes", 90*day)
	orphan := r.blob(t, "orphan bytes", 90*day)
	fresh := r.blob(t, "fresh bytes", time.Hour)
	staged := r.blob(t, "staged sidecar", 90*day)
	gone := r.blob(t, "only on an expired row", 90*day)
	put := func(id, room, key string, age time.Duration) {
		if err := r.b.Attachments.PutAttachment(actx, store.Attachment{ID: id, RoomID: room, Uploader: "maya", Filename: "f", ContentType: "text/plain", BlobKey: key, CreatedAtMs: r.now.Add(-age).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	put("a-old", "r1", shared, 60*day)
	put("a-new", "r2", shared, time.Hour)
	put("a-gone", "r1", gone, 60*day)
	if err := r.b.Settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: "ext.sidecar.demo", Value: `{"artifacts":{"linux":{"url":"https://gw.example/v1/artifacts/` + staged + `"}}}`, UpdatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.b.Tenant.SetTenantRetention(ctx, "acme", wire.RetentionPolicy{MessagesDays: days(31)}); err != nil {
		t.Fatal(err)
	}

	rep, err := r.w.RunOnce(ctx)
	if err != nil || !rep.BlobGC {
		t.Fatalf("RunOnce = %+v, %v", rep, err)
	}
	if rep.Deleted[store.PurgedAttachments] != 2 || rep.Deleted[store.PurgedBlobs] != 2 {
		t.Fatalf("report = %v", rep.Deleted)
	}
	for key, want := range map[string]bool{shared: true, fresh: true, staged: true, orphan: false, gone: false} {
		if r.exists(key) != want {
			t.Fatalf("blob %s exists=%v, want %v", key, !want, want)
		}
	}

	// The collection runs once per interval, not every pass.
	orphan2 := r.blob(t, "another orphan", 90*day)
	r.now = r.now.Add(time.Hour)
	if rep, err = r.w.RunOnce(ctx); err != nil || rep.BlobGC || !r.exists(orphan2) {
		t.Fatalf("second pass within the interval = %+v, %v", rep, err)
	}
	r.now = r.now.Add(DefaultBlobGCInterval)
	if rep, err = r.w.RunOnce(ctx); err != nil || !rep.BlobGC || r.exists(orphan2) {
		t.Fatalf("pass after the interval = %+v, %v", rep, err)
	}
}

// staleWalk reports every blob as old on the walk, as if the walk ran just
// before someone stored the same bytes again.
type staleWalk struct {
	blobstore.BlobStore
	at time.Time
}

func (s staleWalk) Walk(ctx context.Context, fn func(blobstore.Info) error) error {
	return s.BlobStore.Walk(ctx, func(i blobstore.Info) error {
		i.ModTime = s.at
		return fn(i)
	})
}

// A blob stored again after the walk saw it old is kept: the delete
// re-checks the blob's time first.
func TestBlobCollectionRechecksBeforeDelete(t *testing.T) {
	r := newRig(t)
	key := r.blob(t, "re-uploaded", time.Hour)
	r.w.Blobs = staleWalk{BlobStore: r.w.Blobs, at: r.now.Add(-90 * day)}
	n, _, err := r.w.CollectBlobs(context.Background())
	if err != nil || n != 0 || !r.exists(key) {
		t.Fatalf("collect = %d, %v; exists=%v", n, err, r.exists(key))
	}
}

// The whole pass on MySQL (BNBLOC_TEST_MYSQL_DSN, a database of its own): a
// tenant override, the purge with its fence, and the blob collection of the
// rows it removed.
func TestWorkerMySQL(t *testing.T) {
	b, err := store.NewBackends(testmysql.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	dir := t.TempDir()
	blobs, err := blobstore.NewDiskStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{b: b, now: time.Now(), dir: dir}
	r.w = &Worker{Store: store.NewRetention(b), Tenants: b.Tenant, Settings: b.Settings, Audit: b.ExtAudit, Blobs: blobs, Now: func() time.Time { return r.now }}
	ctx := context.Background()
	tid := "acme-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := b.Tenant.PutTenant(ctx, store.Tenant{TenantID: tid, Name: "Acme", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Tenant.SetTenantRetention(ctx, tid, wire.RetentionPolicy{MessagesDays: days(31)}); err != nil {
		t.Fatal(err)
	}
	room, quiet := tid+"-room", tid+"-quiet"
	r.msg(t, tid, room, 1, 60*day)
	r.msg(t, tid, room, 2, time.Hour)
	r.msg(t, tid, quiet, 1, 60*day)
	tctx := tenant.NewContext(ctx, tid)
	oldBlob := r.blob(t, "old attachment "+tid, 60*day)
	if err := b.Attachments.PutAttachment(tctx, store.Attachment{ID: tid + "-att", RoomID: room, Uploader: "maya", Filename: "f", ContentType: "text/plain", BlobKey: oldBlob, CreatedAtMs: r.now.Add(-60 * day).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	rep, err := r.w.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Ran {
		t.Fatal("the retention pass did not take its database's lock")
	}
	if got := r.history(t, tid, room); len(got) != 1 || got[0] != 2 {
		t.Fatalf("history = %v", got)
	}
	if mx, err := b.Chat.MaxRoomSeq(tctx, quiet, ""); err != nil || mx != 1 {
		t.Fatalf("quiet room max = %d, %v", mx, err)
	}
	if _, found, _ := b.Attachments.GetAttachment(tctx, room, tid+"-att"); found {
		t.Fatal("the expired attachment row survived")
	}
	// The shared database's status may say a collection ran recently (an
	// earlier run), so collect explicitly.
	if _, _, err := r.w.CollectBlobs(ctx); err != nil {
		t.Fatal(err)
	}
	if r.exists(oldBlob) {
		t.Fatal("the blob of the expired attachment survived the collection")
	}
}

// fakeSearch records PurgeBefore calls.
type fakeSearch struct {
	calls []string
	err   error
}

func (f *fakeSearch) PurgeBefore(_ context.Context, tenantID string, beforeMs int64) (int64, error) {
	f.calls = append(f.calls, tenantID+"@"+strconv.FormatInt(beforeMs, 10))
	return 3, f.err
}

// After each tenant's message purge the search index drops every document
// older than the same cutoff (only tenants whose policy limits messages); a
// failing index fails the pass but never undoes or blocks the row purge.
func TestWorkerPurgesSearchIndex(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.b.Tenant.SetTenantRetention(ctx, "acme", wire.RetentionPolicy{MessagesDays: days(31), AuditDays: days(40)}); err != nil {
		t.Fatal(err)
	}
	r.msg(t, "acme", "acme-room", 1, 60*day)
	r.msg(t, "acme", "acme-room", 2, time.Hour)
	idx := &fakeSearch{}
	r.w.Search = idx
	rep, err := r.w.RunOnce(ctx)
	if err != nil || !rep.Ran {
		t.Fatalf("RunOnce = %+v, %v", rep, err)
	}
	want := "acme@" + strconv.FormatInt(r.now.Add(-31*day).UnixMilli(), 10)
	if len(idx.calls) != 1 || idx.calls[0] != want {
		t.Fatalf("search purges = %v, want [%s] (globex keeps messages forever)", idx.calls, want)
	}
	if rep.Deleted[store.PurgedSearchDocuments] != 3 || rep.Deleted[store.PurgedRoomMessages] != 1 {
		t.Fatalf("report = %v", rep.Deleted)
	}

	idx.err = errors.New("cluster down")
	r.msg(t, "acme", "acme-room", 3, 50*day)
	r.msg(t, "acme", "acme-room", 4, time.Minute)
	rep, err = r.w.RunOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "search index") {
		t.Fatalf("RunOnce with a failing index err = %v", err)
	}
	if got := r.history(t, "acme", "acme-room"); len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Fatalf("rows after a failed index purge = %v, want the purge done anyway", got)
	}
}

// A pass deletes expired content drafts (a quick try's staging, no policy)
// before the blob collection, so a bundle only an expired draft held goes
// in the same pass while a live draft's stays.
func TestWorkerPurgesExpiredDrafts(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	live := r.blob(t, "live draft bundle", 90*day)
	expired := r.blob(t, "expired draft bundle", 90*day)
	for _, d := range []store.ContentDraft{
		{ID: "cd_live", BundleBlobKey: live, ExpiresAtMs: r.now.Add(time.Hour).UnixMilli()},
		{ID: "cd_expired", BundleBlobKey: expired, ExpiresAtMs: r.now.Add(-time.Minute).UnixMilli()},
	} {
		d.TenantID, d.RoomID, d.JobID, d.Kind, d.Name, d.Version, d.ManifestJSON = "acme", "r1", "job-1", "bnbloc.expert.package/v1", "x", "1.0.0", json.RawMessage(`{}`)
		if err := r.b.ContentComponents.PutContentDraft(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := r.w.RunOnce(ctx)
	if err != nil || rep.Deleted[store.PurgedContentDrafts] != 1 {
		t.Fatalf("RunOnce = %+v, %v", rep, err)
	}
	if _, err := r.b.ContentComponents.GetContentDraft(ctx, "cd_expired"); err == nil {
		t.Fatal("the expired draft is still stored")
	}
	if !r.exists(live) || r.exists(expired) {
		t.Fatalf("blobs: live=%v expired=%v; want only the live draft's bundle kept", r.exists(live), r.exists(expired))
	}
}
