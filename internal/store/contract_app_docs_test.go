package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
)

// appFixture is one app in a fresh room of a fresh tenant, with its key.
type appFixture struct {
	ctx   context.Context
	scope AppScope
	key   string
}

func newAppFixture(t *testing.T, b *Backends) appFixture {
	t.Helper()
	ctx := tenantCtx(uniq("t-app-"))
	room := newContractRoom(t, ctx, b, "maya")
	scope := AppScope{AppID: uniq("app-"), RoomID: room}
	k, err := b.AppDocs.EnsureAppKey(ctx, AppKey{AppID: scope.AppID, RoomID: room, WrappedKey: uniq("wk-"), CreatedAtMs: 1})
	if err != nil {
		t.Fatalf("EnsureAppKey: %v", err)
	}
	return appFixture{ctx: ctx, scope: scope, key: k.WrappedKey}
}

func (f appFixture) doc(collection, id, by string, public bool, body string) AppDoc {
	return AppDoc{AppID: f.scope.AppID, RoomID: f.scope.RoomID, Collection: collection, DocID: id, ByKey: by, Public: public,
		AuthorSealed: []byte("a:" + by), BodySealed: []byte(body), CreatedAtMs: 10, UpdatedAtMs: 10}
}

func (f appFixture) insert(t *testing.T, b *Backends, d AppDoc) {
	t.Helper()
	if err := b.AppDocs.InsertAppDoc(f.ctx, d, f.key); err != nil {
		t.Fatalf("insert %s/%s: %v", d.Collection, d.DocID, err)
	}
}

func docIDs(docs []AppDoc) string {
	s := ""
	for i, d := range docs {
		if i > 0 {
			s += ","
		}
		s += d.DocID
	}
	return s
}

// A key is created once, only in a live room, and an app id is bound to the
// room and tenant that keyed it.
func TestContractAppDocsKeyScope(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		f := newAppFixture(t, b)
		again, err := b.AppDocs.EnsureAppKey(f.ctx, AppKey{AppID: f.scope.AppID, RoomID: f.scope.RoomID, WrappedKey: "other", CreatedAtMs: 2})
		if err != nil || again.WrappedKey != f.key || again.CreatedAtMs != 1 {
			t.Fatalf("second EnsureAppKey = %+v, %v; want the first key kept", again, err)
		}
		if k, ok, err := b.AppDocs.GetAppKey(f.ctx, f.scope); err != nil || !ok || k.WrappedKey != f.key || k.RoomID != f.scope.RoomID {
			t.Fatalf("GetAppKey = %+v %v %v", k, ok, err)
		}
		if _, err := b.AppDocs.EnsureAppKey(f.ctx, AppKey{AppID: uniq("app-"), RoomID: uniq("no-room-"), WrappedKey: "k"}); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("key in a missing room = %v, want ErrRoomNotFound", err)
		}
		other := newContractRoom(t, f.ctx, b, "maya")
		if _, err := b.AppDocs.EnsureAppKey(f.ctx, AppKey{AppID: f.scope.AppID, RoomID: other, WrappedKey: "k"}); !errors.Is(err, ErrAppScope) {
			t.Fatalf("the app keyed from another room = %v, want ErrAppScope", err)
		}
		foreign := tenantCtx(uniq("t-other-"))
		foreignRoom := newContractRoom(t, foreign, b, "jack")
		if _, err := b.AppDocs.EnsureAppKey(foreign, AppKey{AppID: f.scope.AppID, RoomID: foreignRoom, WrappedKey: "k"}); !errors.Is(err, ErrAppScope) {
			t.Fatalf("the app keyed from another tenant = %v, want ErrAppScope", err)
		}
		for _, c := range []struct {
			ctx   context.Context
			scope AppScope
		}{{f.ctx, AppScope{AppID: f.scope.AppID, RoomID: other}}, {foreign, AppScope{AppID: f.scope.AppID, RoomID: f.scope.RoomID}}} {
			if _, ok, err := b.AppDocs.GetAppKey(c.ctx, c.scope); err != nil || ok {
				t.Fatalf("GetAppKey from another scope = %v, %v; want none", ok, err)
			}
		}
		deleted := newContractRoom(t, f.ctx, b, "maya")
		if err := b.Room.DeleteRoom(f.ctx, deleted); err != nil {
			t.Fatal(err)
		}
		if _, err := b.AppDocs.EnsureAppKey(f.ctx, AppKey{AppID: uniq("app-"), RoomID: deleted, WrappedKey: "k"}); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("key in a deleted room = %v, want ErrRoomNotFound", err)
		}
	})
}

// Inserts are create-only per id, need the app's current key, and a find
// narrows by metadata in the store: readability, author, cursor and order.
func TestContractAppDocsInsertFind(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		f := newAppFixture(t, b)
		f.insert(t, b, f.doc("votes", "a", "p1", false, "1"))
		f.insert(t, b, f.doc("votes", "b", "p2", false, "2"))
		f.insert(t, b, f.doc("votes", "c", "p2", true, "3"))
		f.insert(t, b, f.doc("votes", "d", "p1", true, "4"))
		f.insert(t, b, f.doc("options", "a", "p1", true, "o"))
		if err := b.AppDocs.InsertAppDoc(f.ctx, f.doc("votes", "a", "p3", true, "x"), f.key); !errors.Is(err, ErrAppDocExists) {
			t.Fatalf("insert of a taken id = %v, want ErrAppDocExists", err)
		}
		if err := b.AppDocs.InsertAppDoc(f.ctx, f.doc("votes", "e", "p1", false, "x"), "stale"); !errors.Is(err, ErrAppKeyChanged) {
			t.Fatalf("insert under a stale key = %v, want ErrAppKeyChanged", err)
		}
		d, ok, err := b.AppDocs.GetAppDoc(f.ctx, AppDocRef{AppScope: f.scope, Collection: "votes", DocID: "a"})
		if err != nil || !ok || string(d.BodySealed) != "1" || d.Version != 1 || d.ByKey != "p1" || d.Public || string(d.AuthorSealed) != "a:p1" ||
			d.CreatedAtMs != 10 || d.UpdatedAtMs != 10 || d.Collection != "votes" || d.DocID != "a" {
			t.Fatalf("GetAppDoc = %+v %v %v", d, ok, err)
		}
		find := func(q AppDocQuery) string {
			t.Helper()
			q.AppScope, q.Collection = f.scope, "votes"
			return docIDs(must(b.AppDocs.FindAppDocs(f.ctx, q))(t))
		}
		for _, c := range []struct {
			name string
			q    AppDocQuery
			want string
		}{
			{"owner", AppDocQuery{AllDocs: true}, "a,b,c,d"},
			{"reader p1: public and own", AppDocQuery{ReaderKey: "p1"}, "a,c,d"},
			{"reader p2", AppDocQuery{ReaderKey: "p2"}, "b,c,d"},
			{"a stranger reads public only", AppDocQuery{ReaderKey: "p9"}, "c,d"},
			{"by p2 as owner", AppDocQuery{AllDocs: true, By: "p2"}, "b,c"},
			{"by p2 as p1", AppDocQuery{ReaderKey: "p1", By: "p2"}, "c"},
			{"after b", AppDocQuery{AllDocs: true, After: "b"}, "c,d"},
			{"desc", AppDocQuery{AllDocs: true, Desc: true}, "d,c,b,a"},
			{"desc after c", AppDocQuery{AllDocs: true, Desc: true, After: "c"}, "b,a"},
			{"limit", AppDocQuery{AllDocs: true, Limit: 2}, "a,b"},
		} {
			if got := find(c.q); got != c.want {
				t.Errorf("%s: find = %q, want %q", c.name, got, c.want)
			}
		}
		if _, err := b.AppDocs.FindAppDocs(f.ctx, AppDocQuery{AppScope: f.scope, Collection: "votes"}); !errors.Is(err, ErrAppDocQueryUnscoped) {
			t.Fatalf("a find with no reader = %v, want ErrAppDocQueryUnscoped", err)
		}
		// Another app, room or tenant never reads these rows.
		g := newAppFixture(t, b)
		g.insert(t, b, g.doc("votes", "z", "p1", true, "z"))
		for _, c := range []struct {
			ctx   context.Context
			scope AppScope
		}{
			{f.ctx, g.scope},
			{f.ctx, AppScope{AppID: f.scope.AppID, RoomID: g.scope.RoomID}},
			{g.ctx, f.scope},
		} {
			got := must(b.AppDocs.FindAppDocs(c.ctx, AppDocQuery{AppScope: c.scope, Collection: "votes", AllDocs: true}))(t)
			for _, d := range got {
				if d.AppID == f.scope.AppID && d.RoomID == f.scope.RoomID && d.DocID != "z" {
					t.Fatalf("scope %+v read f's document %s", c.scope, d.DocID)
				}
			}
			if _, ok, _ := b.AppDocs.GetAppDoc(c.ctx, AppDocRef{AppScope: c.scope, Collection: "votes", DocID: "a"}); ok && c.scope != f.scope {
				t.Fatalf("scope %+v got f's document a", c.scope)
			}
		}
		counts := must(b.AppDocs.CountAppDocs(f.ctx, f.scope))(t)
		if counts.Docs != 5 || counts.Collections["votes"] != 4 || counts.Collections["options"] != 1 || counts.Bytes != 5 {
			t.Fatalf("CountAppDocs = %+v", counts)
		}
	})
}

// The locked update is the one write: version + 1, fn's error aborts, and
// concurrent read-modify-writes lose nothing.
func TestContractAppDocsLockedUpdate(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		f := newAppFixture(t, b)
		f.insert(t, b, f.doc("tally", "n", "p1", false, "0"))
		ref := AppDocRef{AppScope: f.scope, Collection: "tally", DocID: "n"}
		got, err := b.AppDocs.UpdateAppDoc(f.ctx, ref, func(key string, cur AppDoc) (AppDoc, error) {
			if key != f.key || cur.Version != 1 {
				return AppDoc{}, fmt.Errorf("fn got key %q, version %d", key, cur.Version)
			}
			cur.BodySealed, cur.Public, cur.UpdatedAtMs = []byte("0"), true, 20
			cur.ByKey, cur.CreatedAtMs = "forged", 99 // the store keeps these
			return cur, nil
		})
		if err != nil || got.Version != 2 || !got.Public || got.UpdatedAtMs != 20 || got.ByKey != "p1" || got.CreatedAtMs != 10 {
			t.Fatalf("UpdateAppDoc = %+v, %v", got, err)
		}
		refused := errors.New("refused")
		if _, err := b.AppDocs.UpdateAppDoc(f.ctx, ref, func(string, AppDoc) (AppDoc, error) { return AppDoc{}, refused }); !errors.Is(err, refused) {
			t.Fatalf("an aborted update = %v", err)
		}
		if d, _ := must2(b.AppDocs.GetAppDoc(f.ctx, ref))(t); d.Version != 2 || d.ByKey != "p1" || d.CreatedAtMs != 10 {
			t.Fatalf("after an aborted update: %+v", d)
		}
		missing := ref
		missing.DocID = "nope"
		if _, err := b.AppDocs.UpdateAppDoc(f.ctx, missing, func(_ string, c AppDoc) (AppDoc, error) { return c, nil }); !errors.Is(err, ErrAppDocNotFound) {
			t.Fatalf("update of a missing document = %v", err)
		}

		const writers, each = 8, 5
		var wg sync.WaitGroup
		errs := make(chan error, writers*each)
		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range each {
					_, err := b.AppDocs.UpdateAppDoc(f.ctx, ref, func(_ string, cur AppDoc) (AppDoc, error) {
						n, err := strconv.Atoi(string(cur.BodySealed))
						if err != nil {
							return AppDoc{}, err
						}
						cur.BodySealed = []byte(strconv.Itoa(n + 1))
						return cur, nil
					})
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent update: %v", err)
			}
		}
		d, _ := must2(b.AppDocs.GetAppDoc(f.ctx, ref))(t)
		if string(d.BodySealed) != strconv.Itoa(writers*each) || d.Version != 2+writers*each {
			t.Fatalf("after %d concurrent increments: body %s, version %d", writers*each, d.BodySealed, d.Version)
		}
	})
}

// Racing inserts of one id: exactly one is stored.
func TestContractAppDocsInsertOnce(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		f := newAppFixture(t, b)
		const racers = 12
		var wg sync.WaitGroup
		results := make(chan error, racers)
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results <- b.AppDocs.InsertAppDoc(f.ctx, f.doc("ballots", "me", "p1", false, strconv.Itoa(i)), f.key)
			}()
		}
		wg.Wait()
		close(results)
		won := 0
		for err := range results {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ErrAppDocExists):
				t.Fatalf("racing insert: %v", err)
			}
		}
		if won != 1 {
			t.Fatalf("%d racing inserts succeeded, want exactly 1", won)
		}
	})
}

// A delete runs its check on the locked document; a wipe destroys the key
// and every document of the app, and only that app's.
func TestContractAppDocsDeleteAndWipe(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		f := newAppFixture(t, b)
		for i := range 5 {
			f.insert(t, b, f.doc("c", strconv.Itoa(i), "p1", false, "x"))
		}
		ref := AppDocRef{AppScope: f.scope, Collection: "c", DocID: "0"}
		refused := errors.New("not yours")
		if err := b.AppDocs.DeleteAppDoc(f.ctx, ref, func(cur AppDoc) error {
			if cur.ByKey != "p1" {
				return fmt.Errorf("check got %+v", cur)
			}
			return refused
		}); !errors.Is(err, refused) {
			t.Fatalf("refused delete = %v", err)
		}
		if _, ok, _ := b.AppDocs.GetAppDoc(f.ctx, ref); !ok {
			t.Fatal("a refused delete deleted")
		}
		if err := b.AppDocs.DeleteAppDoc(f.ctx, ref, func(AppDoc) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := b.AppDocs.DeleteAppDoc(f.ctx, ref, nil); !errors.Is(err, ErrAppDocNotFound) {
			t.Fatalf("second delete = %v, want ErrAppDocNotFound", err)
		}

		g := newAppFixture(t, b)
		g.insert(t, b, g.doc("c", "kept", "p1", false, "x"))
		n, err := b.AppDocs.WipeApp(f.ctx, f.scope)
		if err != nil || n != 4 {
			t.Fatalf("WipeApp = %d, %v; want 4", n, err)
		}
		if _, ok, err := b.AppDocs.GetAppKey(f.ctx, f.scope); err != nil || ok {
			t.Fatalf("key after wipe = %v, %v", ok, err)
		}
		if c := must(b.AppDocs.CountAppDocs(f.ctx, f.scope))(t); c.Docs != 0 {
			t.Fatalf("documents after wipe: %+v", c)
		}
		if err := b.AppDocs.InsertAppDoc(f.ctx, f.doc("c", "new", "p1", false, "x"), f.key); !errors.Is(err, ErrAppKeyChanged) {
			t.Fatalf("insert under the destroyed key = %v, want ErrAppKeyChanged", err)
		}
		k, err := b.AppDocs.EnsureAppKey(f.ctx, AppKey{AppID: f.scope.AppID, RoomID: f.scope.RoomID, WrappedKey: "fresh", CreatedAtMs: 5})
		if err != nil || k.WrappedKey != "fresh" {
			t.Fatalf("a write after the wipe mints a new key: %+v, %v", k, err)
		}
		if c := must(b.AppDocs.CountAppDocs(g.ctx, g.scope))(t); c.Docs != 1 {
			t.Fatalf("another app's documents after the wipe: %+v", c)
		}

		if b.DB != nil {
			// A key being destroyed refuses every write (MySQL only: the
			// memory wipe is atomic and never shows one).
			if _, err := b.DB.Exec(`UPDATE app_keys SET wrapped_key = '' WHERE app_id = ?`, g.scope.AppID); err != nil {
				t.Fatal(err)
			}
			if err := b.AppDocs.InsertAppDoc(g.ctx, g.doc("c", "late", "p1", false, "x"), g.key); !errors.Is(err, ErrAppWiping) {
				t.Fatalf("insert while the key is destroyed = %v, want ErrAppWiping", err)
			}
			if _, err := b.AppDocs.EnsureAppKey(g.ctx, AppKey{AppID: g.scope.AppID, RoomID: g.scope.RoomID, WrappedKey: "k"}); !errors.Is(err, ErrAppWiping) {
				t.Fatalf("EnsureAppKey while the key is destroyed = %v, want ErrAppWiping", err)
			}
			// A wipe resumes an interrupted one.
			if n, err := b.AppDocs.WipeApp(g.ctx, g.scope); err != nil || n != 1 {
				t.Fatalf("resumed wipe = %d, %v", n, err)
			}
		}
	})
}

// A master-key rotation re-wraps every key in place.
func TestContractAppDocsRewrapKeys(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		f, g := newAppFixture(t, b), newAppFixture(t, b)
		seen := map[string]bool{}
		n, err := b.AppDocs.RewrapAppKeys(f.ctx, func(k AppKey) (string, error) {
			seen[k.AppID] = true
			return "re:" + k.WrappedKey, nil
		})
		if err != nil || n < 2 || !seen[f.scope.AppID] || !seen[g.scope.AppID] {
			t.Fatalf("RewrapAppKeys = %d, %v (saw %v)", n, err, seen)
		}
		for _, x := range []appFixture{f, g} {
			if k, _, _ := b.AppDocs.GetAppKey(x.ctx, x.scope); k.WrappedKey != "re:"+x.key {
				t.Fatalf("re-wrapped key = %q, want %q", k.WrappedKey, "re:"+x.key)
			}
		}
		boom := errors.New("boom")
		if _, err := b.AppDocs.RewrapAppKeys(f.ctx, func(AppKey) (string, error) { return "", boom }); !errors.Is(err, boom) {
			t.Fatalf("a failed re-wrap = %v", err)
		}
	})
}

// A file is stored once per id under the app's current key, read only in
// its own scope, counted with the app's usage, and deleted with the app's
// data and with its room.
func TestContractAppFiles(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		f := newAppFixture(t, b)
		file := AppFile{AppID: f.scope.AppID, FileID: uniq("f-"), RoomID: f.scope.RoomID, ByKey: "p1", NameSealed: []byte("n"), AuthorSealed: []byte("a"),
			ContentType: "image/png", Size: 300, BlobKey: uniqBlobKey(), CreatedAtMs: 7}
		if err := b.AppDocs.InsertAppFile(f.ctx, file, "not-the-key"); !errors.Is(err, ErrAppKeyChanged) {
			t.Fatalf("insert under another key = %v, want ErrAppKeyChanged", err)
		}
		if err := b.AppDocs.InsertAppFile(f.ctx, file, f.key); err != nil {
			t.Fatal(err)
		}
		if err := b.AppDocs.InsertAppFile(f.ctx, file, f.key); !errors.Is(err, ErrAppDocExists) {
			t.Fatalf("second insert = %v, want ErrAppDocExists", err)
		}
		ref := AppFileRef{AppScope: f.scope, FileID: file.FileID}
		got, ok, err := b.AppDocs.GetAppFile(f.ctx, ref)
		if err != nil || !ok || got.ByKey != "p1" || got.Size != 300 || string(got.NameSealed) != "n" || got.BlobKey != file.BlobKey || got.TenantID == "" {
			t.Fatalf("GetAppFile = %+v %v %v", got, ok, err)
		}
		other := newContractRoom(t, f.ctx, b, "maya")
		if _, ok, err := b.AppDocs.GetAppFile(f.ctx, AppFileRef{AppScope: AppScope{AppID: f.scope.AppID, RoomID: other}, FileID: file.FileID}); err != nil || ok {
			t.Fatalf("file read from another room = %v, %v", ok, err)
		}
		if _, ok, err := b.AppDocs.GetAppFile(tenantCtx(uniq("t-other-")), ref); err != nil || ok {
			t.Fatalf("file read from another tenant = %v, %v", ok, err)
		}
		gone := file
		gone.FileID = uniq("f-")
		if err := b.AppDocs.InsertAppFile(f.ctx, gone, f.key); err != nil {
			t.Fatal(err)
		}
		goneRef := AppFileRef{AppScope: f.scope, FileID: gone.FileID}
		refused := errors.New("not yours")
		if err := b.AppDocs.DeleteAppFile(f.ctx, goneRef, func(cur AppFile) error {
			if cur.ByKey != "p1" {
				return fmt.Errorf("check got %+v", cur)
			}
			return refused
		}); !errors.Is(err, refused) {
			t.Fatalf("refused delete = %v", err)
		}
		if err := b.AppDocs.DeleteAppFile(f.ctx, goneRef, nil); err != nil {
			t.Fatal(err)
		}
		if err := b.AppDocs.DeleteAppFile(f.ctx, goneRef, nil); !errors.Is(err, ErrAppDocNotFound) {
			t.Fatalf("second delete = %v, want ErrAppDocNotFound", err)
		}
		f.insert(t, b, f.doc("c", "d", "p1", false, "xy"))
		if c := must(b.AppDocs.CountAppDocs(f.ctx, f.scope))(t); c.Files != 1 || c.FileBytes != 300 || c.Docs != 1 {
			t.Fatalf("counts = %+v", c)
		}
		if _, err := b.AppDocs.WipeApp(f.ctx, f.scope); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := b.AppDocs.GetAppFile(f.ctx, ref); ok {
			t.Fatal("a file outlived the wipe")
		}

		g := newAppFixture(t, b)
		gf := file
		gf.AppID, gf.RoomID, gf.FileID = g.scope.AppID, g.scope.RoomID, uniq("f-")
		if err := b.AppDocs.InsertAppFile(g.ctx, gf, g.key); err != nil {
			t.Fatal(err)
		}
		if err := b.Room.DeleteRoom(g.ctx, g.scope.RoomID); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := b.AppDocs.GetAppFile(g.ctx, AppFileRef{AppScope: g.scope, FileID: gf.FileID}); ok {
			t.Fatal("a file outlived its room")
		}
	})
}
