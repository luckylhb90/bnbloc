package room

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// App data: the data owner of interactive apps (docs/prd/2026-10-12-
// interactive-apps.md §4–§5). Every published site (an app; its id is the
// site artifact's id) owns one data space of collections of JSON documents
// in the artifact's room and tenant, stored by store.AppDocs and sealed by
// appdata.Cipher.
//
// # Internal contract (the gateway is the only caller)
//
// Routes, all under svcauth.RequireService for the "gateway" service:
//
//	POST   /internal/v1/app/c/{collection}       insert       wire.AppDocWrite      → 201 document
//	GET    /internal/v1/app/c/{collection}       find         ?filter=&sort=&limit=&after= → wire.AppDocPage
//	GET    /internal/v1/app/c/{collection}/{id}  read                               → document
//	PUT    /internal/v1/app/c/{collection}/{id}  replace      wire.AppDocWrite      → document (201 when created)
//	PATCH  /internal/v1/app/c/{collection}/{id}  merge, $inc  wire.AppDocPatch      → document
//	DELETE /internal/v1/app/c/{collection}/{id}  delete                             → 204
//	GET    /internal/v1/app/me                   who am I                           → wire.AppMe
//	GET    /internal/v1/app/stats                owners: usage                      → wire.AppDataStats
//	GET    /internal/v1/app/export               owners: ?collection=&format=json|csv → the data, streamed (audited)
//	DELETE /internal/v1/app/data                 owners: delete all data            → wire.AppDataWiped
//	…/internal/v1/app/files…                     the app's files (appfiles.go)
//
// The scope comes ONLY from the request's signed assertions, never from the
// path, query or body (no route names an app, room or tenant):
//
//   - the identity assertion (svcauth.Key.Assert / NewRequest): subject
//     svcauth.ServiceSubject("gateway"), tenant = the room's tenant;
//   - the app scope (svcauth.Key.AssertApp, after Assert, same timestamp):
//     X-Bnbloc-App-Room, X-Bnbloc-App (the site artifact id) and
//     X-Bnbloc-App-Participant "<kind>:<id>" — member:<subject> for a member
//     ticket, agent:<agent id> for a room agent's job, visitor:<visitor id>
//     for a public share link — signed in X-bnbloc-App-Sig.
//
// The gateway resolves the capability (member ticket, share link, agent
// session) to that scope (delivery step 3); this service re-checks that the
// app is a site artifact of that room in that tenant, and a key is only
// created in a live room.
//
// Conditional writes use HTTP headers: If-Match: <version> is ifVersion (409
// when the document moved on), If-None-Match: * on PUT is ifAbsent (409 when
// the id exists; the primary key decides). A document reads as its JSON
// fields plus the stamps the server owns (_id, _by, _createdAt, _updatedAt,
// _version; _author for owners only): appdata.Render. Errors are httpx's one
// body.
//
// Every write announces itself on the room's stream as a transient
// app_changed event (announceAppChange): wire.AppChanged, metadata only.
//
// # The two rules (§4.4)
//
// The app's owners are the room's members who may act in it
// (membership.Member.MayChat: not a visitor) and the room's agents.
//  1. Only a document's author or an owner may change or delete it.
//  2. A document is readable only by its author and the owners, unless it
//     was written public.
//
// Everything else (who may take part, one vote per person, closing,
// validation) is the app's own code.
func (s *Service) registerAppDataRoutes(mux *http.ServeMux) {
	key := svcauth.KeyFromEnv()
	route := func(pattern string, need appNeed, h appHandler) {
		mux.Handle(pattern, svcauth.RequireService(key, []string{"gateway"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.serveApp(w, r, key, need, h)
		})))
	}
	route("POST /internal/v1/app/c/{collection}", appWrite, s.handleAppInsert)
	route("GET /internal/v1/app/c/{collection}", appRead, s.handleAppFind)
	route("GET /internal/v1/app/c/{collection}/{id}", appRead, s.handleAppGet)
	route("PUT /internal/v1/app/c/{collection}/{id}", appWrite, s.handleAppPut)
	route("PATCH /internal/v1/app/c/{collection}/{id}", appWrite, s.handleAppPatch)
	route("DELETE /internal/v1/app/c/{collection}/{id}", appRead, s.handleAppDelete)
	route("GET /internal/v1/app/me", appWrite, s.handleAppMe)
	route("GET /internal/v1/app/stats", appOwner, s.handleAppStats)
	route("GET /internal/v1/app/export", appOwnerRead, s.handleAppExport)
	route("DELETE /internal/v1/app/data", appOwner, s.handleAppWipe)
	s.registerAppFileRoutes(route)
}

// appNeed is what a route needs of the app before its handler runs.
type appNeed int

const (
	// appRead uses the app's key when it has one (none: no documents).
	appRead appNeed = iota
	// appWrite creates the key on the app's first write.
	appWrite
	// appOwner is an owner's route (no key needed).
	appOwner
	// appOwnerRead is an owner's route reading documents (the key when
	// the app has one).
	appOwnerRead
)

type appHandler func(w http.ResponseWriter, r *http.Request, a *appCall)

// appCall is one request's resolved scope: the only source of the tenant,
// room, app and participant a handler acts in.
type appCall struct {
	ctx         context.Context
	tenantID    string
	scope       store.AppScope
	kind        svcauth.AppParticipantKind
	participant string // the scope's ParticipantKey
	subject     string // a member participant's subject
	owner       bool
	// key and me (the caller's pseudonym) are set when the app has a key.
	key     *appdata.DataKey
	wrapped string
	me      string
}

// serveApp resolves the request's app scope and runs h in it.
func (s *Service) serveApp(w http.ResponseWriter, r *http.Request, key string, need appNeed, h appHandler) {
	if s.AppDocs == nil || s.AppCipher == nil || s.Artifacts == nil {
		httpx.Unavailable(w, "app data")
		return
	}
	scope, err := svcauth.AppScopeOf(key, r)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	tid, err := tenant.Require(ctx)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	a := &appCall{ctx: ctx, tenantID: tid, scope: store.AppScope{AppID: scope.AppID, RoomID: scope.RoomID}, kind: scope.Kind, participant: scope.ParticipantKey()}
	// The app is a site artifact of the scope's room, in its tenant.
	art, err := s.Artifacts.GetArtifact(ctx, scope.AppID)
	switch {
	case errors.Is(err, store.ErrArtifactNotFound) || (err == nil && (art.RoomID != scope.RoomID || art.Kind != store.ArtifactKindSite)):
		httpx.Error(w, "app not found", http.StatusNotFound)
		return
	case err != nil:
		obs.ServerError(w, "read app artifact failed", err)
		httpx.Error(w, "read app failed", http.StatusInternalServerError)
		return
	}
	if a.owner, err = s.appOwner(ctx, scope); err != nil {
		obs.ServerError(w, "read app owner failed", err)
		httpx.Error(w, "read app owner failed", http.StatusInternalServerError)
		return
	}
	if scope.Kind == svcauth.AppMember {
		a.subject = scope.Participant
	}
	if (need == appOwner || need == appOwnerRead) && !a.owner {
		httpx.Error(w, "only the app's owners may do this", http.StatusForbidden)
		return
	}
	if need != appOwner {
		if err := s.loadAppKey(a, need == appWrite); err != nil {
			writeAppError(w, err)
			return
		}
	}
	h(w, r, a)
}

// appOwner is the one owner rule (§4.4): the room's agents, and the room's
// members who may act in it.
func (s *Service) appOwner(ctx context.Context, scope svcauth.AppScope) (bool, error) {
	switch scope.Kind {
	case svcauth.AppAgent:
		return true, nil
	case svcauth.AppMember:
		m, ok, err := s.Store.GetMember(ctx, scope.RoomID, scope.Participant)
		if err != nil || !ok {
			return false, err
		}
		return membership.Member{Role: m.Role, AgentHost: m.AgentHost}.MayChat(), nil
	}
	return false, nil
}

// loadAppKey sets a's key, creating it when create and the app has none.
func (s *Service) loadAppKey(a *appCall, create bool) error {
	k, ok, err := s.AppDocs.GetAppKey(a.ctx, a.scope)
	if err != nil {
		return err
	}
	if !ok && create {
		wrapped, err := s.AppCipher.NewKey(a.tenantID, a.scope.RoomID, a.scope.AppID)
		if err != nil {
			return err
		}
		if k, err = s.AppDocs.EnsureAppKey(a.ctx, store.AppKey{AppID: a.scope.AppID, RoomID: a.scope.RoomID, WrappedKey: wrapped, CreatedAtMs: s.Now()}); err != nil {
			return err
		}
		ok = true
	}
	if !ok {
		a.key, a.wrapped, a.me = nil, "", ""
		return nil
	}
	dk, err := s.AppCipher.Key(k)
	if err != nil {
		return err
	}
	a.key, a.wrapped, a.me = dk, k.WrappedKey, dk.Pseudonym(a.participant)
	return nil
}

// The refusals of the app data routes.
var (
	errAppNotYours      = errors.New("only the document's author or the app's owners may change it")
	errAppVersion       = errors.New("the document changed (version mismatch): read it again")
	errAppAbsentVersion = errors.New("the document does not exist (version mismatch)")
	errAppFull          = errors.New("the app's data is full")
)

// writeAppError is the one status mapping of the app data routes.
func writeAppError(w http.ResponseWriter, err error) {
	status, msg := http.StatusInternalServerError, "app data failed"
	switch {
	case errors.Is(err, appdata.ErrTooLarge):
		status, msg = http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, appdata.ErrInvalid):
		status, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, errAppNotYours):
		status, msg = http.StatusForbidden, err.Error()
	case errors.Is(err, store.ErrAppDocNotFound):
		status, msg = http.StatusNotFound, "document not found"
	case errors.Is(err, store.ErrRoomNotFound), errors.Is(err, store.ErrAppScope):
		status, msg = http.StatusNotFound, "app not found"
	case errors.Is(err, store.ErrAppDocExists):
		status, msg = http.StatusConflict, "document exists"
	case errors.Is(err, errAppVersion), errors.Is(err, errAppAbsentVersion), errors.Is(err, errAppFull):
		status, msg = http.StatusConflict, err.Error()
	case errors.Is(err, store.ErrAppWiping), errors.Is(err, store.ErrAppKeyChanged):
		status, msg = http.StatusConflict, "the app's data is being deleted: retry"
	}
	httpx.WriteStatus(w, status, msg, err)
}

// appPath reads the collection (and id) of a document route.
func appPath(r *http.Request, withID bool) (collection, id string, err error) {
	collection = r.PathValue("collection")
	if !appdata.ValidCollection(collection) {
		return "", "", appdataInvalid("collection name")
	}
	if withID {
		if id = r.PathValue("id"); !appdata.ValidDocID(id) {
			return "", "", appdataInvalid("document id")
		}
	}
	return collection, id, nil
}

func appdataInvalid(what string) error { return fmt.Errorf("%w: %s", appdata.ErrInvalid, what) }

// ifVersion reads If-Match: the version a write requires (0 = none).
func ifVersion(r *http.Request) (int64, error) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		return 0, appdataInvalid("If-Match takes a document version")
	}
	return v, nil
}

// ifAbsent reads If-None-Match: * (create only).
func ifAbsent(r *http.Request) bool { return strings.TrimSpace(r.Header.Get("If-None-Match")) == "*" }

// readable is rule 2: the author, an owner, or a public document.
func (a *appCall) readable(d store.AppDoc) bool {
	return a.owner || d.Public || (a.me != "" && d.ByKey == a.me)
}

// mayChange is rule 1: the author or an owner. A document the caller may
// not even read is not found to them.
func (a *appCall) mayChange(d store.AppDoc) error {
	switch {
	case a.owner || (a.me != "" && d.ByKey == a.me):
		return nil
	case d.Public:
		return errAppNotYours
	}
	return store.ErrAppDocNotFound
}

// render is a stored document as the caller reads it.
func (a *appCall) render(d store.AppDoc) (map[string]any, error) {
	plain, err := a.key.Body(d)
	if err != nil {
		return nil, err
	}
	body, err := appdata.Decode(plain)
	if err != nil {
		return nil, err
	}
	st := appdata.Stamps{ID: d.DocID, By: d.ByKey, CreatedAt: d.CreatedAtMs, UpdatedAt: d.UpdatedAtMs, Version: d.Version}
	if a.owner {
		if st.Author, err = a.key.Author(d); err != nil {
			return nil, err
		}
	}
	return appdata.Render(body, st), nil
}

func (s *Service) writeAppDoc(w http.ResponseWriter, a *appCall, status int, d store.AppDoc) {
	doc, err := a.render(d)
	if err != nil {
		writeAppError(w, err)
		return
	}
	httpx.WriteJSON(w, status, doc)
}

// announceAppChange publishes the transient app_changed event of a write
// (version 0: deleted; no collection: the app was wiped) on the room's
// stream, which each of the app's change feeds narrows to its app (the
// gateway's GET ~app/events). It carries no content. The write has
// happened, so a client that hangs up does not cancel the announcement.
func (s *Service) announceAppChange(a *appCall, collection, id string, version int64) {
	if s.Publisher == nil {
		return
	}
	s.Publisher.Publish(context.WithoutCancel(a.ctx), wire.Event{
		RoomID:   a.scope.RoomID,
		Kind:     wire.EventAppChanged,
		Payload:  wire.AppChanged{AppID: a.scope.AppID, Collection: collection, ID: id, Version: version},
		AtUnixMs: s.Now(),
	})
}

// create seals and inserts a new document by the caller. The app's key is
// re-read once if a wipe replaced it meanwhile.
func (s *Service) createAppDoc(a *appCall, collection, id string, body appdata.Body, public bool) (store.AppDoc, error) {
	plain, err := body.Encode()
	if err != nil {
		return store.AppDoc{}, err
	}
	for attempt := 0; ; attempt++ {
		now := s.Now()
		d := store.AppDoc{AppID: a.scope.AppID, RoomID: a.scope.RoomID, Collection: collection, DocID: id, ByKey: a.me,
			Public: public, Version: 1, CreatedAtMs: now, UpdatedAtMs: now}
		if err := a.key.SealBody(&d, plain); err != nil {
			return store.AppDoc{}, err
		}
		if err := a.key.SealAuthor(&d, a.participant); err != nil {
			return store.AppDoc{}, err
		}
		usage, err := s.appUsage.snapshot(a.ctx, s.AppDocs, a.tenantID, a.scope, now)
		if err != nil {
			return store.AppDoc{}, err
		}
		if err := s.appCaps.admit(usage, collection, 1, int64(len(d.BodySealed))); err != nil {
			return store.AppDoc{}, err
		}
		err = s.AppDocs.InsertAppDoc(a.ctx, d, a.wrapped)
		if errors.Is(err, store.ErrAppKeyChanged) && attempt == 0 {
			if err := s.loadAppKey(a, true); err != nil {
				return store.AppDoc{}, err
			}
			continue
		}
		if err != nil {
			return store.AppDoc{}, err
		}
		s.appUsage.add(a.tenantID, a.scope, collection, 1, int64(len(d.BodySealed)))
		s.announceAppChange(a, collection, id, d.Version)
		return d, nil
	}
}

// update runs one locked write of a document: the two rules and ifVersion
// on the locked row, then next's body sealed in place.
func (s *Service) updateAppDoc(a *appCall, collection, id string, version int64, public *bool, next func(cur appdata.Body) (appdata.Body, error)) (store.AppDoc, error) {
	now := s.Now()
	usage, err := s.appUsage.snapshot(a.ctx, s.AppDocs, a.tenantID, a.scope, now)
	if err != nil {
		return store.AppDoc{}, err
	}
	var grew int64
	ref := store.AppDocRef{AppScope: a.scope, Collection: collection, DocID: id}
	d, err := s.AppDocs.UpdateAppDoc(a.ctx, ref, func(wrapped string, cur store.AppDoc) (store.AppDoc, error) {
		if wrapped != a.wrapped {
			return store.AppDoc{}, store.ErrAppKeyChanged // wiped and re-keyed since the request began
		}
		if err := a.mayChange(cur); err != nil {
			return store.AppDoc{}, err
		}
		if version != 0 && cur.Version != version {
			return store.AppDoc{}, errAppVersion
		}
		plain, err := a.key.Body(cur)
		if err != nil {
			return store.AppDoc{}, err
		}
		body, err := appdata.Decode(plain)
		if err != nil {
			return store.AppDoc{}, err
		}
		body, err = next(body)
		if err != nil {
			return store.AppDoc{}, err
		}
		if plain, err = body.Encode(); err != nil {
			return store.AppDoc{}, err
		}
		out := cur
		if err := a.key.SealBody(&out, plain); err != nil {
			return store.AppDoc{}, err
		}
		grew = int64(len(out.BodySealed) - len(cur.BodySealed))
		if err := s.appCaps.admit(usage, collection, 0, grew); err != nil {
			return store.AppDoc{}, err
		}
		if public != nil {
			out.Public = *public
		}
		out.UpdatedAtMs = now
		return out, nil
	})
	if err != nil {
		return store.AppDoc{}, err
	}
	s.appUsage.add(a.tenantID, a.scope, collection, 0, grew)
	s.announceAppChange(a, collection, id, d.Version)
	return d, nil
}

// handleAppInsert is POST …/c/{collection}: a new document with a server id.
func (s *Service) handleAppInsert(w http.ResponseWriter, r *http.Request, a *appCall) {
	collection, _, err := appPath(r, false)
	if err != nil {
		writeAppError(w, err)
		return
	}
	var in wire.AppDocWrite
	if !httpx.DecodeJSON(w, r, wire.MaxAppRequestBytes, &in) {
		return
	}
	body, err := appdata.ParseBody(in.Doc)
	if err != nil {
		writeAppError(w, err)
		return
	}
	d, err := s.createAppDoc(a, collection, appdata.NewDocID(), body, in.Public != nil && *in.Public)
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.writeAppDoc(w, a, http.StatusCreated, d)
}

// handleAppPut is PUT …/c/{collection}/{id}: create-only with
// If-None-Match: *, else replace (If-Match: version), creating the
// document when it does not exist and no version was required.
func (s *Service) handleAppPut(w http.ResponseWriter, r *http.Request, a *appCall) {
	collection, id, err := appPath(r, true)
	if err != nil {
		writeAppError(w, err)
		return
	}
	version, err := ifVersion(r)
	if err != nil {
		writeAppError(w, err)
		return
	}
	var in wire.AppDocWrite
	if !httpx.DecodeJSON(w, r, wire.MaxAppRequestBytes, &in) {
		return
	}
	body, err := appdata.ParseBody(in.Doc)
	if err != nil {
		writeAppError(w, err)
		return
	}
	create := func() {
		d, err := s.createAppDoc(a, collection, id, body, in.Public != nil && *in.Public)
		if err != nil {
			writeAppError(w, err)
			return
		}
		s.writeAppDoc(w, a, http.StatusCreated, d)
	}
	if ifAbsent(r) {
		create()
		return
	}
	replace := func(appdata.Body) (appdata.Body, error) { return body, nil }
	ref := store.AppDocRef{AppScope: a.scope, Collection: collection, DocID: id}
	for attempt := 0; ; attempt++ {
		d, err := s.updateAppDoc(a, collection, id, version, in.Public, replace)
		if errors.Is(err, store.ErrAppDocNotFound) && version != 0 {
			err = errAppAbsentVersion
		}
		if errors.Is(err, store.ErrAppDocNotFound) {
			// Absent, or someone else's private document (which stays
			// not found): create it only when the id is free.
			_, exists, gerr := s.AppDocs.GetAppDoc(a.ctx, ref)
			if gerr != nil {
				err = gerr
			} else if !exists {
				created, cerr := s.createAppDoc(a, collection, id, body, in.Public != nil && *in.Public)
				if errors.Is(cerr, store.ErrAppDocExists) && attempt == 0 {
					continue // created meanwhile: replace it
				}
				if cerr != nil {
					writeAppError(w, cerr)
					return
				}
				s.writeAppDoc(w, a, http.StatusCreated, created)
				return
			}
		}
		if err != nil {
			writeAppError(w, err)
			return
		}
		s.writeAppDoc(w, a, http.StatusOK, d)
		return
	}
}

// handleAppPatch is PATCH …/c/{collection}/{id}: merge and $inc, applied
// on the locked document (concurrent $inc lose nothing).
func (s *Service) handleAppPatch(w http.ResponseWriter, r *http.Request, a *appCall) {
	collection, id, err := appPath(r, true)
	if err != nil {
		writeAppError(w, err)
		return
	}
	version, err := ifVersion(r)
	if err != nil {
		writeAppError(w, err)
		return
	}
	var in wire.AppDocPatch
	if !httpx.DecodeJSON(w, r, wire.MaxAppRequestBytes, &in) {
		return
	}
	d, err := s.updateAppDoc(a, collection, id, version, in.Public, func(cur appdata.Body) (appdata.Body, error) {
		return cur.Patch(in.Merge, in.Inc)
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.writeAppDoc(w, a, http.StatusOK, d)
}

// handleAppDelete is DELETE …/c/{collection}/{id} (If-Match: version).
func (s *Service) handleAppDelete(w http.ResponseWriter, r *http.Request, a *appCall) {
	collection, id, err := appPath(r, true)
	if err != nil {
		writeAppError(w, err)
		return
	}
	version, err := ifVersion(r)
	if err != nil {
		writeAppError(w, err)
		return
	}
	if a.key == nil && !a.owner {
		writeAppError(w, store.ErrAppDocNotFound) // no key: no document, and no pseudonym to own one
		return
	}
	var size int
	err = s.AppDocs.DeleteAppDoc(a.ctx, store.AppDocRef{AppScope: a.scope, Collection: collection, DocID: id}, func(cur store.AppDoc) error {
		if err := a.mayChange(cur); err != nil {
			return err
		}
		if version != 0 && cur.Version != version {
			return errAppVersion
		}
		size = len(cur.BodySealed)
		return nil
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.appUsage.add(a.tenantID, a.scope, collection, -1, -int64(size))
	s.announceAppChange(a, collection, id, 0)
	w.WriteHeader(http.StatusNoContent)
}

// handleAppGet is GET …/c/{collection}/{id}.
func (s *Service) handleAppGet(w http.ResponseWriter, r *http.Request, a *appCall) {
	collection, id, err := appPath(r, true)
	if err != nil {
		writeAppError(w, err)
		return
	}
	if a.key == nil {
		writeAppError(w, store.ErrAppDocNotFound)
		return
	}
	d, ok, err := s.AppDocs.GetAppDoc(a.ctx, store.AppDocRef{AppScope: a.scope, Collection: collection, DocID: id})
	if err == nil && (!ok || !a.readable(d)) {
		err = store.ErrAppDocNotFound
	}
	if err != nil {
		writeAppError(w, err)
		return
	}
	doc, err := a.render(d)
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.auditAppRead(a, appReadGet, collection, "", 1, nil)
	httpx.WriteJSON(w, http.StatusOK, doc)
}

// A find decrypts what SQL narrowed and filters it here: it scans at most
// appFindMaxScan documents per request, appFindBatch at a time, and hands
// back a cursor when it stopped early.
const (
	appFindBatch   = 200
	appFindMaxScan = 2000
)

// handleAppFind is GET …/c/{collection}?filter=&sort=&limit=&after=.
// sort is _id (default) or -_id; limit is clamped to wire.MaxAppFindLimit.
func (s *Service) handleAppFind(w http.ResponseWriter, r *http.Request, a *appCall) {
	collection, _, err := appPath(r, false)
	if err != nil {
		writeAppError(w, err)
		return
	}
	query := r.URL.Query()
	filter, err := appdata.ParseFilter(query.Get("filter"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	q := store.AppDocQuery{AppScope: a.scope, Collection: collection, AllDocs: a.owner, ReaderKey: a.me, After: query.Get("after")}
	switch query.Get("sort") {
	case "", appdata.StampID:
	case "-" + appdata.StampID:
		q.Desc = true
	default:
		writeAppError(w, appdataInvalid("sort is _id or -_id"))
		return
	}
	if q.After != "" && !appdata.ValidDocID(q.After) {
		writeAppError(w, appdataInvalid("after is a document id"))
		return
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 {
			writeAppError(w, appdataInvalid("limit is a positive number"))
			return
		}
	}
	page, err := s.findAppDocs(a, q, filter, min(limit, wire.MaxAppFindLimit))
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.auditAppRead(a, appReadFind, collection, "", len(page.Docs), nil)
	httpx.WriteJSON(w, http.StatusOK, page)
}

// findAppDocs is one page of a find: up to limit documents matching filter,
// scanning at most appFindMaxScan.
func (s *Service) findAppDocs(a *appCall, q store.AppDocQuery, filter appdata.Filter, limit int) (wire.AppDocPage, error) {
	page := wire.AppDocPage{Docs: []map[string]any{}}
	if a.key == nil {
		return page, nil
	}
	if by, ok := filter.By(); ok {
		q.By = by
	}
	q.Limit = appFindBatch
	if filter.Empty() {
		q.Limit = limit
	}
	for scanned := 0; ; {
		docs, err := s.AppDocs.FindAppDocs(a.ctx, q)
		if err != nil {
			return wire.AppDocPage{}, err
		}
		for _, d := range docs {
			doc, err := a.render(d)
			if err != nil {
				return wire.AppDocPage{}, err
			}
			q.After = d.DocID
			if filter.Match(doc) {
				page.Docs = append(page.Docs, doc)
			}
			if len(page.Docs) == limit {
				page.Next = q.After
				return page, nil
			}
		}
		scanned += len(docs)
		if len(docs) < q.Limit {
			return page, nil
		}
		if scanned >= appFindMaxScan {
			page.Next = q.After
			return page, nil
		}
	}
}

// handleAppMe is GET …/me: the caller's per-app id, and a signed-in
// caller's own display name and account (login, the email their identity
// provider asserted). Only ever the caller's own: no route answers another
// participant's account to the page.
func (s *Service) handleAppMe(w http.ResponseWriter, _ *http.Request, a *appCall) {
	me := wire.AppMe{ID: a.me}
	if a.subject != "" && s.Profiles != nil {
		names, err := s.displayNames(a.ctx, []string{a.subject})
		if err != nil {
			obs.ServerError(w, "read display name failed", err)
			httpx.Error(w, "read display name failed", http.StatusInternalServerError)
			return
		}
		me.Name = names[0].DisplayName
		email, err := s.Profiles.Email(a.ctx, a.subject)
		if err != nil {
			obs.ServerError(w, "read account failed", err)
			httpx.Error(w, "read account failed", http.StatusInternalServerError)
			return
		}
		me.Account = &wire.AppAccount{Login: a.subject, Email: email}
	}
	httpx.WriteJSON(w, http.StatusOK, me)
}

// handleAppStats is GET …/stats (owners).
func (s *Service) handleAppStats(w http.ResponseWriter, _ *http.Request, a *appCall) {
	c, err := s.AppDocs.CountAppDocs(a.ctx, a.scope)
	if err != nil {
		writeAppError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.AppDataStats{Documents: c.Docs, Bytes: c.Bytes, Collections: c.Collections, Files: c.Files, FileBytes: c.FileBytes})
}

// handleAppWipe is DELETE …/data (owners): crypto-shreds all of the app's
// data (store.AppDocs.WipeApp).
func (s *Service) handleAppWipe(w http.ResponseWriter, _ *http.Request, a *appCall) {
	n, err := s.AppDocs.WipeApp(a.ctx, a.scope)
	s.appUsage.forget(a.tenantID, a.scope)
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.announceAppChange(a, "", "", 0)
	httpx.WriteJSON(w, http.StatusOK, wire.AppDataWiped{Deleted: n})
}

// appCaps are the per-app safety caps (§4.5): the wire constants, lowered
// only by tests.
type appCaps struct {
	docs, bytes, collections int64
	// files, fileBytes bound the app's uploaded files (admitFile).
	files, fileBytes int64
}

var defaultAppCaps = appCaps{docs: wire.MaxAppDocs, bytes: wire.MaxAppBytes, collections: wire.MaxAppCollections, files: wire.MaxAppFiles, fileBytes: wire.MaxAppFileBytes}

// admit checks a write adding docs documents and bytes stored bytes to
// collection against usage.
func (c appCaps) admit(u store.AppCounts, collection string, docs, bytes int64) error {
	if c == (appCaps{}) {
		c = defaultAppCaps
	}
	switch {
	case docs > 0 && u.Docs+docs > c.docs:
		return fmt.Errorf("%w: %d documents", errAppFull, c.docs)
	case bytes > 0 && u.Bytes+bytes > c.bytes:
		return fmt.Errorf("%w: %d bytes", errAppFull, c.bytes)
	case docs > 0 && u.Collections[collection] == 0 && int64(len(u.Collections))+1 > c.collections:
		return fmt.Errorf("%w: %d collections", errAppFull, c.collections)
	}
	return nil
}

// appUsageTTLMs is how long a replica trusts its count of an app's usage
// before reading it again; between reads it adds its own writes. Counting
// reads the whole app, so it is not done per write: the caps may be
// overshot by what other replicas write within the window, which bounds
// cost and abuse all the same.
const appUsageTTLMs = 10_000

// maxAppUsageEntries bounds the usage cache.
const maxAppUsageEntries = 4096

type appUsageEntry struct {
	counts store.AppCounts
	atMs   int64
}

// appUsageCache is a replica's view of each app's usage.
type appUsageCache struct {
	mu sync.Mutex
	m  map[string]*appUsageEntry
}

func appUsageKey(tid string, scope store.AppScope) string { return tid + "\x00" + scope.AppID }

// snapshot returns the app's usage, re-counted when stale.
func (c *appUsageCache) snapshot(ctx context.Context, docs store.AppDocs, tid string, scope store.AppScope, nowMs int64) (store.AppCounts, error) {
	k := appUsageKey(tid, scope)
	c.mu.Lock()
	e, ok := c.m[k]
	if ok && nowMs-e.atMs < appUsageTTLMs {
		out := copyCounts(e.counts)
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()
	counts, err := docs.CountAppDocs(ctx, scope)
	if err != nil {
		return store.AppCounts{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*appUsageEntry{}
	}
	if len(c.m) >= maxAppUsageEntries {
		clear(c.m)
	}
	c.m[k] = &appUsageEntry{counts: counts, atMs: nowMs}
	return copyCounts(counts), nil
}

// add records a write of this replica.
func (c *appUsageCache) add(tid string, scope store.AppScope, collection string, docs, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[appUsageKey(tid, scope)]
	if !ok {
		return
	}
	e.counts.Docs += docs
	e.counts.Bytes += bytes
	if e.counts.Collections == nil {
		e.counts.Collections = map[string]int64{}
	}
	if e.counts.Collections[collection] += docs; e.counts.Collections[collection] <= 0 {
		delete(e.counts.Collections, collection)
	}
}

// addFile records a file this replica stored (size < 0: deleted).
func (c *appUsageCache) addFile(tid string, scope store.AppScope, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[appUsageKey(tid, scope)]
	if !ok {
		return
	}
	if size >= 0 {
		e.counts.Files++
	} else {
		e.counts.Files--
	}
	e.counts.FileBytes += size
}

// forget drops an app's usage (it was wiped).
func (c *appUsageCache) forget(tid string, scope store.AppScope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, appUsageKey(tid, scope))
}

func copyCounts(c store.AppCounts) store.AppCounts {
	out := store.AppCounts{Docs: c.Docs, Bytes: c.Bytes, Collections: make(map[string]int64, len(c.Collections)), Files: c.Files, FileBytes: c.FileBytes}
	for k, v := range c.Collections {
		out.Collections[k] = v
	}
	return out
}
