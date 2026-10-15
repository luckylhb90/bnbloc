package appdata

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

func TestMain(m *testing.M) { os.Exit(testmysql.Main(m)) }

var (
	mysqlOnce sync.Once
	mysqlB    *store.Backends
	mysqlErr  error
)

// forBackends runs fn on fresh memory backends and, when
// BNBLOC_TEST_MYSQL_DSN is set, on the test binary's MySQL database.
func forBackends(t *testing.T, fn func(t *testing.T, b *store.Backends)) {
	t.Helper()
	mem, err := store.NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("memory", func(t *testing.T) { fn(t, mem) })
	if !testmysql.Enabled() {
		return
	}
	mysqlOnce.Do(func() {
		dsn, err := testmysql.Shared()
		if err != nil {
			mysqlErr = err
			return
		}
		mysqlB, mysqlErr = store.NewBackends(dsn)
	})
	if mysqlErr != nil {
		t.Fatalf("mysql backends: %v", mysqlErr)
	}
	t.Run("mysql", func(t *testing.T) { fn(t, mysqlB) })
}

// forFreshBackends is forBackends with a MySQL database of the test's own:
// for a test that walks every app's key (a rotation).
func forFreshBackends(t *testing.T, fn func(t *testing.T, b *store.Backends)) {
	t.Helper()
	mem, err := store.NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("memory", func(t *testing.T) { fn(t, mem) })
	if !testmysql.Enabled() {
		return
	}
	t.Run("mysql", func(t *testing.T) {
		b, err := store.NewBackends(testmysql.Fresh(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.DB.Close() })
		fn(t, b)
	})
}

func newBox(t *testing.T) *sealbox.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := sealbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// app is one keyed app in a fresh room of a fresh tenant.
type app struct {
	ctx   context.Context
	scope store.AppScope
	key   store.AppKey
	dk    *DataKey
}

func newApp(t *testing.T, b *store.Backends, c *Cipher) app {
	t.Helper()
	tid := ids.New("t-")
	ctx := tenant.NewContext(context.Background(), tid)
	room := ids.New("room-")
	if err := b.Room.CreateRoomWithOwner(ctx, store.Room{RoomID: room, Name: "r", CreatedBy: "maya", CreatedAtMs: 1},
		store.Membership{RoomID: room, UserID: "maya", Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	scope := store.AppScope{AppID: ids.New("art_"), RoomID: room}
	wrapped, err := c.NewKey(tid, room, scope.AppID)
	if err != nil {
		t.Fatal(err)
	}
	k, err := b.AppDocs.EnsureAppKey(ctx, store.AppKey{AppID: scope.AppID, RoomID: room, WrappedKey: wrapped, CreatedAtMs: 1})
	if err != nil {
		t.Fatal(err)
	}
	dk, err := c.Key(k)
	if err != nil {
		t.Fatal(err)
	}
	return app{ctx: ctx, scope: scope, key: k, dk: dk}
}

// put seals body and author into a new document and stores it.
func (a app) put(t *testing.T, b *store.Backends, collection, id, body, author string) store.AppDoc {
	t.Helper()
	d := store.AppDoc{AppID: a.scope.AppID, RoomID: a.scope.RoomID, Collection: collection, DocID: id,
		ByKey: a.dk.Pseudonym(author), CreatedAtMs: 1, UpdatedAtMs: 1}
	if err := a.dk.SealBody(&d, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := a.dk.SealAuthor(&d, author); err != nil {
		t.Fatal(err)
	}
	if err := b.AppDocs.InsertAppDoc(a.ctx, d, a.key.WrappedKey); err != nil {
		t.Fatal(err)
	}
	got, ok, err := b.AppDocs.GetAppDoc(a.ctx, d.Ref())
	if err != nil || !ok {
		t.Fatalf("read back %s: %v %v", id, ok, err)
	}
	return got
}

func TestCipherRoundTripAndPseudonyms(t *testing.T) {
	forBackends(t, func(t *testing.T, b *store.Backends) {
		c := NewCipher(newBox(t))
		a, other := newApp(t, b, c), newApp(t, b, c)
		d := a.put(t, b, "votes", "v1", `{"choice":"lake"}`, "member:maya")
		if body, err := a.dk.Body(d); err != nil || string(body) != `{"choice":"lake"}` {
			t.Fatalf("Body = %q, %v", body, err)
		}
		if who, err := a.dk.Author(d); err != nil || who != "member:maya" {
			t.Fatalf("Author = %q, %v", who, err)
		}
		if a.dk.Pseudonym("member:maya") != d.ByKey || a.dk.Pseudonym("member:maya") == a.dk.Pseudonym("visitor:maya") {
			t.Fatal("a pseudonym is per participant and stable")
		}
		if a.dk.Pseudonym("member:maya") == other.dk.Pseudonym("member:maya") {
			t.Fatal("one participant has the same pseudonym in two apps")
		}
		if _, err := other.dk.Body(d); err == nil {
			t.Fatal("another app's key opened the document")
		}
		if NewCipher(nil) != nil {
			t.Fatal("no box, no cipher")
		}
	})
}

// A file's name and uploader open under its own app's key, at its own id
// only.
func TestCipherFileNames(t *testing.T) {
	c := NewCipher(newBox(t))
	k1, k2 := mustKey(t, c, "app-1"), mustKey(t, c, "app-2")
	f := store.AppFile{AppID: "app-1", FileID: "f1"}
	if err := k1.SealFile(&f, "receipt.png", "member:maya"); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(f.NameSealed, []byte("receipt")) || bytes.Contains(f.AuthorSealed, []byte("maya")) {
		t.Fatal("a sealed file holds plaintext")
	}
	if n, err := k1.FileName(f); err != nil || n != "receipt.png" {
		t.Fatalf("FileName = %q, %v", n, err)
	}
	if who, err := k1.FileAuthor(f); err != nil || who != "member:maya" {
		t.Fatalf("FileAuthor = %q, %v", who, err)
	}
	moved := f
	moved.FileID = "f2"
	if _, err := k1.FileName(moved); !errors.Is(err, ErrOpen) {
		t.Fatalf("name moved to another file: %v, want ErrOpen", err)
	}
	other := f
	other.AppID = "app-2"
	if _, err := k2.FileName(other); !errors.Is(err, ErrOpen) {
		t.Fatalf("name moved to another app: %v, want ErrOpen", err)
	}
	if err := k2.SealFile(&store.AppFile{AppID: "app-1", FileID: "x"}, "n", "p"); err == nil {
		t.Fatal("sealed a file of another app")
	}
}

func mustKey(t *testing.T, c *Cipher, appID string) *DataKey {
	t.Helper()
	wrapped, err := c.NewKey("t", "room", appID)
	if err != nil {
		t.Fatal(err)
	}
	k, err := c.Key(store.AppKey{AppID: appID, TenantID: "t", RoomID: "room", WrappedKey: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The database never holds a readable body or author: the raw columns are
// ciphertext.
func TestCipherRawColumnsNeverPlaintext(t *testing.T) {
	forBackends(t, func(t *testing.T, b *store.Backends) {
		c := NewCipher(newBox(t))
		a := newApp(t, b, c)
		const secret, who = "the-secret-ballot-text", "member:maya-the-author"
		d := a.put(t, b, "ballots", "b1", `{"text":"`+secret+`"}`, who)
		bodies, authors := [][]byte{d.BodySealed}, [][]byte{d.AuthorSealed}
		if b.DB != nil {
			var body, author []byte
			if err := b.DB.QueryRow(`SELECT body_sealed, author_sealed FROM app_docs WHERE app_id = ? AND tenant_id = ?`,
				a.scope.AppID, tenant.FromContext(a.ctx)).Scan(&body, &author); err != nil {
				t.Fatal(err)
			}
			bodies, authors = append(bodies, body), append(authors, author)
			var wrapped string
			if err := b.DB.QueryRow(`SELECT wrapped_key FROM app_keys WHERE app_id = ? AND tenant_id = ?`, a.scope.AppID, tenant.FromContext(a.ctx)).Scan(&wrapped); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(wrapped, "v1:") {
				t.Fatalf("app_keys.wrapped_key is not sealed: %q", wrapped)
			}
		}
		for _, raw := range bodies {
			if len(raw) == 0 || bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(`"text"`)) {
				t.Fatalf("body_sealed holds plaintext: %q", raw)
			}
		}
		for _, raw := range authors {
			if len(raw) == 0 || bytes.Contains(raw, []byte("maya")) {
				t.Fatalf("author_sealed holds plaintext: %q", raw)
			}
		}
	})
}

// A ciphertext moved to another row (document, collection or app) does not
// open there.
func TestCipherMovedCiphertextFailsToOpen(t *testing.T) {
	forBackends(t, func(t *testing.T, b *store.Backends) {
		c := NewCipher(newBox(t))
		a, other := newApp(t, b, c), newApp(t, b, c)
		src := a.put(t, b, "votes", "mine", `{"v":1}`, "member:maya")
		targets := []store.AppDoc{
			a.put(t, b, "votes", "theirs", `{"v":2}`, "member:jack"),
			a.put(t, b, "other", "mine", `{"v":3}`, "member:jack"),
		}
		for _, dst := range targets {
			moved := move(t, b, a.ctx, dst, src)
			if _, err := a.dk.Body(moved); !errors.Is(err, ErrOpen) {
				t.Fatalf("body moved to %s/%s: %v, want ErrOpen", dst.Collection, dst.DocID, err)
			}
			if _, err := a.dk.Author(moved); !errors.Is(err, ErrOpen) {
				t.Fatalf("author moved to %s/%s: %v, want ErrOpen", dst.Collection, dst.DocID, err)
			}
		}
		// Into another app (same collection and id), under that app's key.
		dst := other.put(t, b, "votes", "mine", `{"v":4}`, "member:jack")
		if _, err := other.dk.Body(move(t, b, other.ctx, dst, src)); !errors.Is(err, ErrOpen) {
			t.Fatalf("body moved to another app: %v, want ErrOpen", err)
		}
		// A wrapped key copied to another app does not unwrap there.
		stolen := other.key
		stolen.WrappedKey = a.key.WrappedKey
		if _, err := c.Key(stolen); err == nil {
			t.Fatal("a wrapped key opened as another app's")
		}
	})
}

// move overwrites dst's sealed columns with src's, as someone with SQL
// access would, and returns dst as stored.
func move(t *testing.T, b *store.Backends, ctx context.Context, dst, src store.AppDoc) store.AppDoc {
	t.Helper()
	if b.DB != nil {
		if _, err := b.DB.Exec(`UPDATE app_docs SET body_sealed = ?, author_sealed = ? WHERE app_id = ? AND collection = ? AND doc_id = ? AND tenant_id = ?`,
			src.BodySealed, src.AuthorSealed, dst.AppID, dst.Collection, dst.DocID, tenant.FromContext(ctx)); err != nil {
			t.Fatal(err)
		}
		got, _, err := b.AppDocs.GetAppDoc(ctx, dst.Ref())
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	dst.BodySealed, dst.AuthorSealed = src.BodySealed, src.AuthorSealed
	return dst
}

// After a wipe the documents are unreadable even when their rows are
// restored (from a backup, say): the key that sealed them is gone, and the
// app's next key does not open them.
func TestCipherWipeShredsRestoredRows(t *testing.T) {
	forBackends(t, func(t *testing.T, b *store.Backends) {
		c := NewCipher(newBox(t))
		a := newApp(t, b, c)
		backup := []store.AppDoc{a.put(t, b, "votes", "1", `{"v":1}`, "member:maya"), a.put(t, b, "votes", "2", `{"v":2}`, "member:jack")}
		if n, err := b.AppDocs.WipeApp(a.ctx, a.scope); err != nil || n != 2 {
			t.Fatalf("WipeApp = %d, %v", n, err)
		}
		wrapped, err := c.NewKey(tenant.FromContext(a.ctx), a.scope.RoomID, a.scope.AppID)
		if err != nil {
			t.Fatal(err)
		}
		k, err := b.AppDocs.EnsureAppKey(a.ctx, store.AppKey{AppID: a.scope.AppID, RoomID: a.scope.RoomID, WrappedKey: wrapped, CreatedAtMs: 2})
		if err != nil {
			t.Fatal(err)
		}
		next, err := c.Key(k)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range backup {
			// Restore the row exactly as it was.
			if err := b.AppDocs.InsertAppDoc(a.ctx, d, k.WrappedKey); err != nil {
				t.Fatalf("restore %s: %v", d.DocID, err)
			}
			restored, ok, err := b.AppDocs.GetAppDoc(a.ctx, d.Ref())
			if err != nil || !ok || !bytes.Equal(restored.BodySealed, d.BodySealed) {
				t.Fatalf("restored row = %v %v", ok, err)
			}
			if _, err := next.Body(restored); !errors.Is(err, ErrOpen) {
				t.Fatalf("a restored row opened after the wipe: %v", err)
			}
			if _, err := next.Author(restored); !errors.Is(err, ErrOpen) {
				t.Fatalf("a restored author opened after the wipe: %v", err)
			}
		}
	})
}

// Rotating the deployment key re-wraps the data keys only: every document
// still opens under the new deployment key, and the old one opens no key.
func TestCipherRotateRewrapsKeysOnly(t *testing.T) {
	forFreshBackends(t, func(t *testing.T, b *store.Backends) {
		from, to := NewCipher(newBox(t)), NewCipher(newBox(t))
		a := newApp(t, b, from)
		d := a.put(t, b, "votes", "1", `{"v":1}`, "member:maya")
		before := d.BodySealed
		if _, err := Rotate(a.ctx, b.AppDocs, from, to); err != nil {
			t.Fatal(err)
		}
		k, ok, err := b.AppDocs.GetAppKey(a.ctx, a.scope)
		if err != nil || !ok || k.WrappedKey == a.key.WrappedKey {
			t.Fatalf("key after rotation = %+v %v %v", k, ok, err)
		}
		if _, err := from.Key(k); err == nil {
			t.Fatal("the old deployment key still unwraps")
		}
		dk, err := to.Key(k)
		if err != nil {
			t.Fatal(err)
		}
		got, _, _ := b.AppDocs.GetAppDoc(a.ctx, d.Ref())
		if !bytes.Equal(got.BodySealed, before) {
			t.Fatal("rotation re-encrypted a document")
		}
		if body, err := dk.Body(got); err != nil || string(body) != `{"v":1}` {
			t.Fatalf("Body after rotation = %q, %v", body, err)
		}
		if dk.Pseudonym("member:maya") != d.ByKey {
			t.Fatal("rotation changed a pseudonym")
		}
	})
}

// TestSealedColumnsWrittenOnlyByCipher: app_docs.body_sealed (and
// author_sealed, and app_files.name_sealed) hold only what Cipher sealed. Outside this package's
// cipher.go nothing assigns AppDoc.BodySealed / AuthorSealed, and outside
// the store (which passes rows through) no Go source names the columns.
func TestSealedColumnsWrittenOnlyByCipher(t *testing.T) {
	assign := regexp.MustCompile(`\.(BodySealed|AuthorSealed|NameSealed)\s*=[^=]|\b(BodySealed|AuthorSealed|NameSealed)\s*:\s*[^=]`)
	column := regexp.MustCompile(`\b(body_sealed|author_sealed|name_sealed)\b`)
	var problems []string
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "cmd", "core"}}, func(rel string, src []byte) {
		if rel == "internal/appdata/cipher.go" || strings.HasPrefix(rel, "internal/store/") {
			return
		}
		if assign.Match(src) {
			problems = append(problems, rel+": sets a sealed AppDoc field (seal through appdata.DataKey)")
		}
		if column.Match(src) {
			problems = append(problems, rel+": names a sealed app_docs column (only the store reads and writes it)")
		}
	})
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("app document bodies are written only by appdata.Cipher:\n  %s", strings.Join(problems, "\n  "))
	}
}
