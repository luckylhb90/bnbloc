package room

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/testmysql"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestMain(m *testing.M) { os.Exit(testmysql.Main(m)) }

const appTestKey = "app-test-internal-key"

var (
	appMySQLOnce sync.Once
	appMySQL     *store.Backends
	appMySQLErr  error
)

// forAppBackends runs fn on fresh memory backends and, when
// BNBLOC_TEST_MYSQL_DSN is set, on the test binary's MySQL database.
func forAppBackends(t *testing.T, fn func(t *testing.T, b *store.Backends)) {
	t.Helper()
	mem, err := store.NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("memory", func(t *testing.T) { fn(t, mem) })
	if !testmysql.Enabled() {
		return
	}
	appMySQLOnce.Do(func() {
		dsn, err := testmysql.Shared()
		if err != nil {
			appMySQLErr = err
			return
		}
		appMySQL, appMySQLErr = store.NewBackends(dsn)
	})
	if appMySQLErr != nil {
		t.Fatalf("mysql backends: %v", appMySQLErr)
	}
	t.Run("mysql", func(t *testing.T) { fn(t, appMySQL) })
}

// appEnv is a room service with app data on, behind svcauth as the
// service's main mounts it, and one room ("maya" owner, "jack" member,
// "vic" visitor) holding the site app.
type appEnv struct {
	t      *testing.T
	b      *store.Backends
	svc    *Service
	srv    *httptest.Server
	tenant string
	room   string
	app    string
}

func newAppEnv(t *testing.T, b *store.Backends) *appEnv {
	t.Helper()
	t.Setenv("BNBLOC_INTERNAL_KEY", appTestKey)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	box, err := sealbox.New(raw)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(b.Room, b.Channel)
	svc.Artifacts, svc.Profiles, svc.AppDocs, svc.AppCipher = b.Artifacts, b.Profiles, b.AppDocs, appdata.NewCipher(box)
	e := &appEnv{t: t, b: b, svc: svc, tenant: ids.New("t-")}
	e.srv = httptest.NewServer(svcauth.Middleware(appTestKey, svc.Handler()))
	t.Cleanup(e.srv.Close)
	e.room = e.newRoom()
	e.app = e.newSite(e.room)
	return e
}

func (e *appEnv) ctx() context.Context { return tenant.NewContext(context.Background(), e.tenant) }

func (e *appEnv) newRoom() string {
	e.t.Helper()
	room := ids.New("room-")
	if err := e.b.Room.CreateRoomWithOwner(e.ctx(), store.Room{RoomID: room, Name: "r", CreatedBy: "maya", CreatedAtMs: 1},
		store.Membership{RoomID: room, UserID: "maya", Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
	for user, role := range map[string]store.Role{"jack": store.RoleMember, "vic": store.RoleVisitor} {
		if err := e.b.Room.AddMember(e.ctx(), store.Membership{RoomID: room, UserID: user, Role: role, JoinedAtMs: 1}); err != nil {
			e.t.Fatal(err)
		}
	}
	return room
}

func (e *appEnv) newArtifact(room, kind string) string {
	e.t.Helper()
	a, err := e.b.Artifacts.PutArtifact(e.ctx(), store.Artifact{ArtifactID: ids.New("art_"), RoomID: room, Kind: kind, Title: "app", CreatedBy: "maya"})
	if err != nil {
		e.t.Fatal(err)
	}
	return a.ArtifactID
}

func (e *appEnv) newSite(room string) string { return e.newArtifact(room, store.ArtifactKindSite) }

// who is a participant of e's app.
type who struct {
	kind svcauth.AppParticipantKind
	id   string
}

var (
	maya    = who{svcauth.AppMember, "maya"} // the room's owner
	jack    = who{svcauth.AppMember, "jack"} // a room member
	vic     = who{svcauth.AppMember, "vic"}  // a room visitor: not an owner
	zed     = who{svcauth.AppMember, "zed"}  // not in the room
	agent   = who{svcauth.AppAgent, "maya/codex"}
	visitA  = who{svcauth.AppVisitor, "v-a"}
	visitB  = who{svcauth.AppVisitor, "v-b"}
	jackVis = who{svcauth.AppVisitor, "jack"} // a visitor id equal to a member's subject
)

// reply is one response.
type reply struct {
	status int
	body   []byte
}

func (r reply) doc() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.body, &m)
	return m
}

func (r reply) page() wire.AppDocPage {
	var p wire.AppDocPage
	_ = json.Unmarshal(r.body, &p)
	return p
}

// do calls the app data API as the gateway would, for p in scope (tenant,
// room, app); headers are extra "Name: value" pairs.
func (e *appEnv) doIn(tenantID, room, app string, p who, method, path string, body any, headers ...string) reply {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	k := svcauth.Key(appTestKey)
	req, err := k.NewRequest(context.Background(), method, e.srv.URL+"/internal/v1/app"+path, rd, svcauth.ServiceSubject("gateway"), tenantID)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := k.AssertApp(req, svcauth.AppScope{RoomID: room, AppID: app, Kind: p.kind, Participant: p.id}); err != nil {
		e.t.Fatal(err)
	}
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ": ")
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: out}
}

func (e *appEnv) do(p who, method, path string, body any, headers ...string) reply {
	e.t.Helper()
	return e.doIn(e.tenant, e.room, e.app, p, method, path, body, headers...)
}

// insert posts doc to collection as p and returns the stored document.
func (e *appEnv) insert(p who, collection string, doc map[string]any, public bool) map[string]any {
	e.t.Helper()
	r := e.do(p, http.MethodPost, "/c/"+collection, map[string]any{"doc": doc, "public": public})
	if r.status != http.StatusCreated {
		e.t.Fatalf("insert as %v = %d %s", p, r.status, r.body)
	}
	return r.doc()
}

func (e *appEnv) me(p who) string {
	e.t.Helper()
	r := e.do(p, http.MethodGet, "/me", nil)
	var me wire.AppMe
	if r.status != http.StatusOK || json.Unmarshal(r.body, &me) != nil || me.ID == "" {
		e.t.Fatalf("me as %v = %d %s", p, r.status, r.body)
	}
	return me.ID
}

func (e *appEnv) find(p who, collection, query string) wire.AppDocPage {
	e.t.Helper()
	r := e.do(p, http.MethodGet, "/c/"+collection+query, nil)
	if r.status != http.StatusOK {
		e.t.Fatalf("find as %v %s = %d %s", p, query, r.status, r.body)
	}
	return r.page()
}

// docIDsOf is a page's ids in page order (doc_id order: server ids sort by
// creation time, and two in one millisecond in either order).
func docIDsOf(p wire.AppDocPage) string {
	var out []string
	for _, d := range p.Docs {
		out = append(out, d["_id"].(string))
	}
	return strings.Join(out, ",")
}

// idSet is ids in doc_id order, to compare with docIDsOf.
func idSet(ids ...string) string {
	slices.Sort(ids)
	return strings.Join(ids, ",")
}

// The scope is the signed capability's: one app's requests never reach
// another app's, room's or tenant's data, and a forged, unsigned or
// non-gateway request is refused.
func TestAppDataCrossAppScope(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		doc := e.insert(maya, "votes", map[string]any{"v": "a"}, true)
		id := doc["_id"].(string)

		appB := e.newSite(e.room)
		otherRoom := e.newRoom()
		appC := e.newSite(otherRoom)
		for name, s := range map[string][2]string{"another app": {e.room, appB}, "another room's app": {otherRoom, appC}} {
			// The other app has data (and a key) of its own.
			if r := e.doIn(e.tenant, s[0], s[1], maya, http.MethodPut, "/c/votes/own", map[string]any{"doc": map[string]any{"v": "b"}}); r.status != http.StatusCreated {
				t.Fatalf("%s: own insert = %d %s", name, r.status, r.body)
			}
			if r := e.doIn(e.tenant, s[0], s[1], maya, http.MethodPut, "/c/votes/"+id, map[string]any{"doc": map[string]any{"v": "b"}}, "If-Match: 1"); r.status != http.StatusConflict {
				t.Fatalf("%s replacing app A's document = %d %s", name, r.status, r.body)
			}
			if r := e.doIn(e.tenant, s[0], s[1], maya, http.MethodDelete, "/c/votes/"+id, nil); r.status != http.StatusNotFound {
				t.Fatalf("%s deleting app A's document = %d %s", name, r.status, r.body)
			}
			if got := e.doIn(e.tenant, s[0], s[1], maya, http.MethodGet, "/c/votes/"+id, nil); got.status != http.StatusNotFound {
				t.Fatalf("%s reading app A's document = %d %s", name, got.status, got.body)
			}
			if r := e.doIn(e.tenant, s[0], s[1], maya, http.MethodGet, "/c/votes", nil); r.status != http.StatusOK || docIDsOf(r.page()) != "own" {
				t.Fatalf("%s listing = %d %s", name, r.status, r.body)
			}
			var wiped wire.AppDataWiped
			if r := e.doIn(e.tenant, s[0], s[1], maya, http.MethodDelete, "/data", nil); r.status != http.StatusOK || json.Unmarshal(r.body, &wiped) != nil || wiped.Deleted != 1 {
				t.Fatalf("%s wipe = %d %s", name, r.status, r.body)
			}
		}
		// A capability naming app A under another room, a non-site
		// artifact, or another tenant reaches nothing.
		notSite := e.newArtifact(e.room, "markdown")
		for name, s := range map[string][3]string{
			"app A in another room":   {e.tenant, otherRoom, e.app},
			"a non-site artifact":     {e.tenant, e.room, notSite},
			"app A in another tenant": {ids.New("t-"), e.room, e.app},
		} {
			if r := e.doIn(s[0], s[1], s[2], maya, http.MethodGet, "/c/votes/"+id, nil); r.status != http.StatusNotFound {
				t.Fatalf("%s = %d %s", name, r.status, r.body)
			}
			if r := e.doIn(s[0], s[1], s[2], maya, http.MethodPost, "/c/votes", map[string]any{"doc": map[string]any{}}); r.status != http.StatusNotFound {
				t.Fatalf("%s insert = %d %s", name, r.status, r.body)
			}
		}
		if got := e.do(maya, http.MethodGet, "/c/votes/"+id, nil); got.status != http.StatusOK {
			t.Fatalf("app A's document after the other scopes = %d %s", got.status, got.body)
		}

		// Unsigned, forged and non-gateway requests.
		forge := func(edit func(*http.Request)) int {
			req, _ := svcauth.Key(appTestKey).NewRequest(context.Background(), http.MethodGet, e.srv.URL+"/internal/v1/app/c/votes", nil, svcauth.ServiceSubject("gateway"), e.tenant)
			_ = svcauth.Key(appTestKey).AssertApp(req, svcauth.AppScope{RoomID: e.room, AppID: e.app, Kind: svcauth.AppVisitor, Participant: "v"})
			edit(req)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		if s := forge(func(r *http.Request) { r.Header.Set(svcauth.HeaderApp, appB) }); s != http.StatusUnauthorized {
			t.Fatalf("a re-pointed app = %d, want 401", s)
		}
		if s := forge(func(r *http.Request) { r.Header.Del(svcauth.HeaderAppSig) }); s != http.StatusUnauthorized {
			t.Fatalf("an unsigned scope = %d, want 401", s)
		}
		if s := forge(func(r *http.Request) { r.Header.Set(svcauth.HeaderAppParticipant, "member:maya") }); s != http.StatusUnauthorized {
			t.Fatalf("a forged participant = %d, want 401", s)
		}
		if s := forge(func(r *http.Request) {
			svcauth.Key(appTestKey).Assert(r, "maya", e.tenant, "")
		}); s != http.StatusForbidden {
			t.Fatalf("a member's own identity = %d, want 403 (gateway only)", s)
		}
	})
}

// The client never sets a stamp: _id, _by, _createdAt, _updatedAt and
// _version are the server's, and _by is the caller's pseudonym.
func TestAppDataStampsAreServerOwned(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		e.svc.Now = func() int64 { return 5000 }
		forged := map[string]any{"_id": "forged", "_by": "someone", "_createdAt": 1, "_updatedAt": 1, "_version": 99, "_author": "x", "v": 1}
		doc := e.insert(visitA, "votes", forged, false)
		me := e.me(visitA)
		if doc["_id"] == "forged" || doc["_by"] != me || doc["_version"] != 1.0 || doc["_createdAt"] != 5000.0 || doc["v"] != 1.0 {
			t.Fatalf("inserted = %v (me %s)", doc, me)
		}
		if _, ok := doc["_author"]; ok {
			t.Fatal("a visitor read _author")
		}
		id := doc["_id"].(string)
		for _, c := range []struct {
			method string
			body   any
		}{
			{http.MethodPut, map[string]any{"doc": forged}},
			{http.MethodPatch, map[string]any{"merge": map[string]any{"_by": "x", "_version": 7, "w": 2}}},
		} {
			r := e.do(visitA, c.method, "/c/votes/"+id, c.body)
			got := r.doc()
			if r.status != http.StatusOK || got["_id"] != id || got["_by"] != me || got["_createdAt"] != 5000.0 {
				t.Fatalf("%s with stamps = %d %v", c.method, r.status, got)
			}
		}
		if r := e.do(visitA, http.MethodPatch, "/c/votes/"+id, map[string]any{"$inc": map[string]any{"_version": 1}}); r.status != http.StatusBadRequest {
			t.Fatalf("$inc on a stamp = %d %s", r.status, r.body)
		}
		if r := e.do(visitA, http.MethodPost, "/c/votes", map[string]any{"doc": map[string]any{"_mine": 1}}); r.status != http.StatusBadRequest {
			t.Fatalf("a reserved field = %d %s", r.status, r.body)
		}
		// Visitors see only the pseudonym; the app's owners see who wrote it.
		if got := e.do(maya, http.MethodGet, "/c/votes/"+id, nil).doc(); !reflect.DeepEqual(got["_author"], map[string]any{"kind": "visitor", "id": "v-a"}) || got["_by"] != me {
			t.Fatalf("the owner's read = %v", got)
		}
		// A member and a visitor with the same id are different participants.
		if e.me(jack) == e.me(jackVis) {
			t.Fatal("member jack and visitor jack share a pseudonym")
		}
	})
}

// Rule 1: only the author or an owner changes or deletes a document.
func TestAppDataNonAuthorUpdateRefused(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		public := e.insert(visitA, "board", map[string]any{"n": 1}, true)["_id"].(string)
		private := e.insert(visitA, "board", map[string]any{"n": 1}, false)["_id"].(string)
		for _, p := range []who{visitB, vic, zed} {
			for _, c := range []struct {
				method string
				body   any
			}{
				{http.MethodPut, map[string]any{"doc": map[string]any{"n": 0}}},
				{http.MethodPatch, map[string]any{"$inc": map[string]any{"n": 1}}},
				{http.MethodDelete, nil},
			} {
				if r := e.do(p, c.method, "/c/board/"+public, c.body); r.status != http.StatusForbidden {
					t.Fatalf("%v %s on another's public document = %d %s", p, c.method, r.status, r.body)
				}
				if r := e.do(p, c.method, "/c/board/"+private, c.body); r.status != http.StatusNotFound {
					t.Fatalf("%v %s on another's private document = %d %s", p, c.method, r.status, r.body)
				}
			}
		}
		if got := e.do(visitA, http.MethodGet, "/c/board/"+public, nil).doc(); got["n"] != 1.0 || got["_version"] != 1.0 {
			t.Fatalf("after refused writes: %v", got)
		}
		// The author and every owner may.
		for i, p := range []who{visitA, maya, jack, agent} {
			if r := e.do(p, http.MethodPatch, "/c/board/"+private, map[string]any{"$inc": map[string]any{"n": 1}}); r.status != http.StatusOK || r.doc()["n"] != float64(i+2) {
				t.Fatalf("%v's patch = %d %s", p, r.status, r.body)
			}
		}
		if r := e.do(agent, http.MethodDelete, "/c/board/"+public, nil); r.status != http.StatusNoContent {
			t.Fatalf("an owner's delete = %d %s", r.status, r.body)
		}
		if r := e.do(visitA, http.MethodDelete, "/c/board/"+private, nil); r.status != http.StatusNoContent {
			t.Fatalf("the author's delete = %d %s", r.status, r.body)
		}
		// PUT on another's private id never creates over it.
		hidden := e.insert(visitA, "board", map[string]any{"n": 1}, false)["_id"].(string)
		if r := e.do(visitB, http.MethodPut, "/c/board/"+hidden, map[string]any{"doc": map[string]any{"n": 9}}); r.status != http.StatusNotFound {
			t.Fatalf("PUT over another's private document = %d %s", r.status, r.body)
		}
	})
}

// Rule 2: a document not written public is hidden from every other holder
// of the app, even against direct calls; owners read everything.
func TestAppDataPrivateHidden(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		ballot := e.insert(visitA, "votes", map[string]any{"choice": "lake"}, false)["_id"].(string)
		option := e.insert(maya, "votes", map[string]any{"option": "lake"}, true)["_id"].(string)
		mine := e.insert(visitB, "votes", map[string]any{"choice": "sea"}, false)["_id"].(string)
		if r := e.do(visitB, http.MethodGet, "/c/votes/"+ballot, nil); r.status != http.StatusNotFound {
			t.Fatalf("another visitor reading a private ballot = %d %s", r.status, r.body)
		}
		if got := docIDsOf(e.find(visitB, "votes", "")); got != idSet(option, mine) {
			t.Fatalf("visitor B lists %q, want the public option and their own ballot", got)
		}
		if got := docIDsOf(e.find(vic, "votes", "")); got != option {
			t.Fatalf("a room visitor lists %q, want the public option only", got)
		}
		for _, p := range []who{maya, jack, agent} {
			page := e.find(p, "votes", "")
			if docIDsOf(page) != idSet(ballot, option, mine) {
				t.Fatalf("owner %v lists %q", p, docIDsOf(page))
			}
			for _, d := range page.Docs {
				if _, ok := d["_author"]; !ok {
					t.Fatalf("owner %v read no _author: %v", p, d)
				}
			}
		}
		for _, d := range e.find(visitB, "votes", "").Docs {
			if _, ok := d["_author"]; ok {
				t.Fatalf("a visitor read _author: %v", d)
			}
		}
		// The raw store row holds no plaintext.
		stored, ok, err := b.AppDocs.GetAppDoc(e.ctx(), store.AppDocRef{AppScope: store.AppScope{AppID: e.app, RoomID: e.room}, Collection: "votes", DocID: ballot})
		if err != nil || !ok || bytes.Contains(stored.BodySealed, []byte("lake")) || bytes.Contains(stored.AuthorSealed, []byte("v-a")) {
			t.Fatalf("stored ballot = %q / %q (%v %v)", stored.BodySealed, stored.AuthorSealed, ok, err)
		}
	})
}

// The filter takes only the whitelist, and narrows after decryption.
func TestAppDataFilterWhitelist(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		for i, choice := range []string{"lake", "sea", "lake", "hill"} {
			e.insert(maya, "votes", map[string]any{"choice": choice, "n": i, "meta": map[string]any{"round": i % 2}}, true)
		}
		visitorDoc := e.insert(visitA, "votes", map[string]any{"choice": "lake", "n": 9}, true)
		for _, bad := range []string{`{"$or":[{"choice":"sea"}]}`, `{"choice":{"$regex":"^l"}}`, `{"$where":"1"}`, `{"n":{"$in":3}}`, `not-json`} {
			if r := e.do(maya, http.MethodGet, "/c/votes?filter="+urlQuery(bad), nil); r.status != http.StatusBadRequest {
				t.Fatalf("filter %s = %d %s", bad, r.status, r.body)
			}
		}
		count := func(filter string) int {
			return len(e.find(maya, "votes", "?filter="+urlQuery(filter)).Docs)
		}
		for filter, want := range map[string]int{
			`{"choice":"lake"}`:                            3,
			`{"choice":{"$in":["sea","hill"]}}`:            2,
			`{"n":{"$gte":1,"$lt":3}}`:                     2,
			`{"meta.round":1}`:                             2,
			`{"meta.round":{"$exists":false}}`:             1,
			`{"choice":{"$ne":"lake"}}`:                    2,
			`{"_by":"` + visitorDoc["_by"].(string) + `"}`: 1,
		} {
			if got := count(filter); got != want {
				t.Errorf("filter %s matched %d, want %d", filter, got, want)
			}
		}
		// Paging with a filter: the cursor resumes after the last scanned.
		first := e.find(maya, "votes", "?limit=2&filter="+urlQuery(`{"choice":"lake"}`))
		if len(first.Docs) != 2 || first.Next == "" {
			t.Fatalf("first page = %+v", first)
		}
		rest := e.find(maya, "votes", "?limit=2&after="+first.Next+"&filter="+urlQuery(`{"choice":"lake"}`))
		all := e.find(maya, "votes", "?filter="+urlQuery(`{"choice":"lake"}`))
		if len(rest.Docs) != 1 || rest.Next != "" || docIDsOf(first)+","+docIDsOf(rest) != docIDsOf(all) {
			t.Fatalf("pages %s + %s, want %s", docIDsOf(first), docIDsOf(rest), docIDsOf(all))
		}
		asc, desc := e.find(maya, "votes", ""), e.find(maya, "votes", "?sort=-_id")
		reversed := slices.Clone(desc.Docs)
		slices.Reverse(reversed)
		if len(asc.Docs) != 5 || docIDsOf(asc) != docIDsOf(wire.AppDocPage{Docs: reversed}) {
			t.Fatalf("-_id is %s, not the reverse of %s", docIDsOf(desc), docIDsOf(asc))
		}
		if r := e.do(maya, http.MethodGet, "/c/votes?sort=choice", nil); r.status != http.StatusBadRequest {
			t.Fatalf("sort by a body field = %d", r.status)
		}
	})
}

func urlQuery(s string) string {
	r := strings.NewReplacer("%", "%25", "&", "%26", "+", "%2B", "#", "%23", " ", "%20", "{", "%7B", "}", "%7D", `"`, "%22")
	return r.Replace(s)
}

// ifVersion: a write that names a version the document moved past is 409.
func TestAppDataIfVersionConflict(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		id := e.insert(maya, "c", map[string]any{"n": 1}, false)["_id"].(string)
		if r := e.do(maya, http.MethodPatch, "/c/c/"+id, map[string]any{"merge": map[string]any{"n": 2}}, "If-Match: 1"); r.status != http.StatusOK || r.doc()["_version"] != 2.0 {
			t.Fatalf("a matching If-Match = %d %s", r.status, r.body)
		}
		for _, c := range []struct {
			method string
			body   any
		}{
			{http.MethodPatch, map[string]any{"merge": map[string]any{"n": 3}}},
			{http.MethodPut, map[string]any{"doc": map[string]any{"n": 3}}},
			{http.MethodDelete, nil},
		} {
			if r := e.do(maya, c.method, "/c/c/"+id, c.body, "If-Match: 1"); r.status != http.StatusConflict {
				t.Fatalf("%s with a stale If-Match = %d %s", c.method, r.status, r.body)
			}
		}
		if r := e.do(maya, http.MethodPut, "/c/c/nope", map[string]any{"doc": map[string]any{}}, "If-Match: 1"); r.status != http.StatusConflict {
			t.Fatalf("If-Match on a missing document = %d %s", r.status, r.body)
		}
		if r := e.do(maya, http.MethodDelete, "/c/c/"+id, nil, "If-Match: 2"); r.status != http.StatusNoContent {
			t.Fatalf("delete with the current version = %d %s", r.status, r.body)
		}
	})
}

// Concurrent $inc on one document lose nothing.
func TestAppDataIncLosesNothing(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		id := e.insert(maya, "tally", map[string]any{"votes": 0}, true)["_id"].(string)
		const n = 24
		var wg sync.WaitGroup
		statuses := make(chan int, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p := []who{maya, jack, agent}[i%3]
				statuses <- e.do(p, http.MethodPatch, "/c/tally/"+id, map[string]any{"$inc": map[string]any{"votes": 1, "by.kind": 2}}).status
			}()
		}
		wg.Wait()
		close(statuses)
		for s := range statuses {
			if s != http.StatusOK {
				t.Fatalf("concurrent $inc = %d", s)
			}
		}
		got := e.do(maya, http.MethodGet, "/c/tally/"+id, nil).doc()
		if got["votes"] != float64(n) || got["by"].(map[string]any)["kind"] != float64(2*n) || got["_version"] != float64(n+1) {
			t.Fatalf("after %d concurrent $inc: %v", n, got)
		}
	})
}

// ifAbsent: racing creates of one id succeed exactly once.
func TestAppDataIfAbsentOnce(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		me := e.me(visitA)
		const n = 12
		var wg sync.WaitGroup
		statuses := make(chan int, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses <- e.do(visitA, http.MethodPut, "/c/ballots/"+me, map[string]any{"doc": map[string]any{"pick": i}}, "If-None-Match: *").status
			}()
		}
		wg.Wait()
		close(statuses)
		created := 0
		for s := range statuses {
			switch s {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
			default:
				t.Fatalf("racing ifAbsent = %d", s)
			}
		}
		if created != 1 {
			t.Fatalf("%d racing ifAbsent creates succeeded, want 1", created)
		}
		if got := e.find(maya, "ballots", ""); len(got.Docs) != 1 || got.Docs[0]["_id"] != me {
			t.Fatalf("ballots = %+v", got)
		}
	})
}

// The per-app caps: a document over 64 KiB is 413; an app at its document,
// byte or collection cap takes no more.
func TestAppDataCaps(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		big := strings.Repeat("x", wire.MaxAppDocBytes)
		if r := e.do(maya, http.MethodPost, "/c/c", map[string]any{"doc": map[string]any{"t": big}}); r.status != http.StatusRequestEntityTooLarge {
			t.Fatalf("an oversized document = %d", r.status)
		}
		id := e.insert(maya, "c", map[string]any{"t": "x"}, false)["_id"].(string)
		if r := e.do(maya, http.MethodPatch, "/c/c/"+id, map[string]any{"merge": map[string]any{"t": big}}); r.status != http.StatusRequestEntityTooLarge {
			t.Fatalf("a patch over the document cap = %d", r.status)
		}
		e.svc.appCaps = appCaps{docs: 3, bytes: 1 << 20, collections: 2}
		e.insert(maya, "c", map[string]any{"t": "x"}, false)
		e.insert(maya, "d", map[string]any{"t": "x"}, false)
		if r := e.do(maya, http.MethodPost, "/c/c", map[string]any{"doc": map[string]any{}}); r.status != http.StatusConflict {
			t.Fatalf("a document past the cap = %d %s", r.status, r.body)
		}
		if r := e.do(maya, http.MethodDelete, "/c/c/"+id, nil); r.status != http.StatusNoContent {
			t.Fatal(r.status)
		}
		if r := e.do(maya, http.MethodPost, "/c/e", map[string]any{"doc": map[string]any{}}); r.status != http.StatusConflict {
			t.Fatalf("a collection past the cap = %d %s", r.status, r.body)
		}
		e.insert(maya, "c", map[string]any{"t": "x"}, false)
		e.svc.appCaps = appCaps{docs: 100, bytes: 400, collections: 10}
		if r := e.do(maya, http.MethodPost, "/c/c", map[string]any{"doc": map[string]any{"t": strings.Repeat("y", 300)}}); r.status != http.StatusConflict {
			t.Fatalf("a document past the byte cap = %d %s", r.status, r.body)
		}
	})
}

// Owners read an app's usage and delete its data; nobody else may. After a
// wipe the app starts over under a new key (and new pseudonyms).
func TestAppDataStatsAndWipe(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		before := e.me(visitA)
		e.insert(visitA, "votes", map[string]any{"v": 1}, false)
		e.insert(maya, "options", map[string]any{"o": 1}, true)
		for _, p := range []who{visitA, vic, zed} {
			if r := e.do(p, http.MethodGet, "/stats", nil); r.status != http.StatusForbidden {
				t.Fatalf("%v stats = %d", p, r.status)
			}
			if r := e.do(p, http.MethodDelete, "/data", nil); r.status != http.StatusForbidden {
				t.Fatalf("%v wipe = %d", p, r.status)
			}
		}
		var stats wire.AppDataStats
		r := e.do(jack, http.MethodGet, "/stats", nil)
		if r.status != http.StatusOK || json.Unmarshal(r.body, &stats) != nil || stats.Documents != 2 || stats.Collections["votes"] != 1 || stats.Bytes == 0 {
			t.Fatalf("stats = %d %s", r.status, r.body)
		}
		r = e.do(agent, http.MethodDelete, "/data", nil)
		var wiped wire.AppDataWiped
		if r.status != http.StatusOK || json.Unmarshal(r.body, &wiped) != nil || wiped.Deleted != 2 {
			t.Fatalf("wipe = %d %s", r.status, r.body)
		}
		if got := e.find(maya, "votes", ""); len(got.Docs) != 0 {
			t.Fatalf("after the wipe: %+v", got)
		}
		if e.me(visitA) == before {
			t.Fatal("a wipe kept the pseudonyms")
		}
		e.insert(visitA, "votes", map[string]any{"v": 2}, false)
	})
}

// me: the caller's pseudonym, and a member's own display name.
func TestAppDataMe(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		if err := b.Profiles.SetDisplayName(e.ctx(), "jack", "Jack J", 1); err != nil {
			t.Fatal(err)
		}
		var me wire.AppMe
		if r := e.do(jack, http.MethodGet, "/me", nil); r.status != http.StatusOK || json.Unmarshal(r.body, &me) != nil || me.Name != "Jack J" {
			t.Fatalf("member me = %d %s", r.status, r.body)
		}
		if doc := e.insert(jack, "c", map[string]any{}, false); doc["_by"] != me.ID {
			t.Fatalf("_by %v is not me().id %s", doc["_by"], me.ID)
		}
		if r := e.do(visitA, http.MethodGet, "/me", nil); r.status != http.StatusOK || strings.Contains(string(r.body), "name") {
			t.Fatalf("visitor me = %d %s", r.status, r.body)
		}
	})
}

// Without the secret store key app data is off, not open.
func TestAppDataOffWithoutKey(t *testing.T) {
	b, err := store.NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	e := newAppEnv(t, b)
	e.svc.AppCipher = nil
	if r := e.do(maya, http.MethodGet, "/me", nil); r.status != http.StatusServiceUnavailable {
		t.Fatalf("app data without a cipher = %d", r.status)
	}
}

// TestAppDataScopeFromCapabilityOnly: the app data routes take their scope
// (tenant, room, app, participant) only from the signed assertions. The
// handlers read no path value but the collection and the id, no query
// parameter but the find's, no header but the conditional ones, and the
// request bodies name no app, room, tenant or participant.
func TestAppDataScopeFromCapabilityOnly(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "appdata.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]map[string]bool{
		"PathValue": {"collection": true, "id": true},
		"Get":       {"filter": true, "sort": true, "limit": true, "after": true, "If-Match": true, "If-None-Match": true},
	}
	var problems []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "PathValue", "Get":
			if len(call.Args) != 1 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				problems = append(problems, sel.Sel.Name+" with a computed name")
				return true
			}
			name, _ := strconv.Unquote(lit.Value)
			if !allowed[sel.Sel.Name][name] {
				problems = append(problems, sel.Sel.Name+"("+lit.Value+")")
			}
		case "FormValue", "PostFormValue", "Cookie":
			problems = append(problems, sel.Sel.Name)
		}
		return true
	})
	for _, v := range []any{wire.AppDocWrite{}, wire.AppDocPatch{}} {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.ToLower(typ.Field(i).Tag.Get("json"))
			for _, scope := range []string{"app", "room", "tenant", "participant", "user", "author"} {
				if strings.Contains(tag, scope) {
					problems = append(problems, typ.Name()+"."+typ.Field(i).Name+" names "+scope)
				}
			}
		}
	}
	if len(problems) > 0 {
		t.Fatalf("app data routes read scope outside the signed capability:\n  %s", strings.Join(problems, "\n  "))
	}
}
