package store

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// App data (docs/prd/2026-10-12-interactive-apps.md §4.1, §5.2): every
// published site (an app; its id is the site artifact's id) owns one data
// space of named collections of JSON documents. One shared table holds every
// app's documents, keyed app_id first, so one app's rows cluster together
// and every query is a range inside its prefix. The store keeps sealed
// bytes and routing metadata only: bodies and author identities are sealed
// by appdata.Cipher under the app's data key, which app_keys holds wrapped.
//
// Every operation is bound to one (tenant, room, app) scope: the tenant from
// the context, the room and app from AppScope, all three in every query. The
// room service takes that scope only from the capability the gateway
// resolved (services/room/appdata.go).

// AppScope names one app's data space inside the context's tenant.
type AppScope struct {
	AppID  string
	RoomID string
}

// AppDocRef names one document.
type AppDocRef struct {
	AppScope
	Collection string
	DocID      string
}

// AppKey is an app's data key, wrapped by the deployment key (sealbox). A
// key row whose WrappedKey is empty is being destroyed (a wipe or a room
// deletion in progress): the app takes no writes until it is gone.
type AppKey struct {
	AppID       string
	TenantID    string
	RoomID      string
	WrappedKey  string
	CreatedAtMs int64
}

// AppDoc is one stored document: routing metadata in the clear, the body
// and the author's real identity sealed (appdata.Cipher is the only writer
// of BodySealed and AuthorSealed).
type AppDoc struct {
	AppID      string
	Collection string
	DocID      string
	TenantID   string
	RoomID     string
	// ByKey is the author's per-app pseudonym (the document's _by).
	ByKey        string
	Public       bool
	AuthorSealed []byte
	// Version is 1 on insert and grows by one with every update.
	Version     int64
	BodySealed  []byte
	CreatedAtMs int64
	UpdatedAtMs int64
}

// Ref is the document's reference.
func (d AppDoc) Ref() AppDocRef {
	return AppDocRef{AppScope: AppScope{AppID: d.AppID, RoomID: d.RoomID}, Collection: d.Collection, DocID: d.DocID}
}

// AppDocQuery is one page of a collection, narrowed by metadata only (the
// body is sealed: a body filter runs on the decrypted page, in the service).
// Pages are in doc_id order (Desc: reverse), after the After cursor.
type AppDocQuery struct {
	AppScope
	Collection string
	// AllDocs reads every document (an app owner). Otherwise only public
	// documents and the reader's own (ReaderKey, their pseudonym) are read;
	// a query with neither is refused (ErrAppDocQueryUnscoped), never
	// widened.
	AllDocs   bool
	ReaderKey string
	// By narrows to one author's documents ("" = any author).
	By    string
	After string
	Desc  bool
	// Limit is clamped to [1, wire.MaxAppFindLimit] (default
	// defaultAppFindLimit).
	Limit int
}

// AppCounts is one app's usage: documents and stored (sealed) bytes, in
// total and per collection.
type AppCounts struct {
	Docs        int64
	Bytes       int64
	Collections map[string]int64
}

// defaultAppFindLimit is a find page without a limit.
const defaultAppFindLimit = 50

// appShredBatch is how many documents one statement of a crypto-shred
// deletes: a large app never holds one long transaction.
const appShredBatch = 1000

// App data errors (both backends).
var (
	// ErrAppDocExists refuses an insert of a document id the collection
	// holds (ifAbsent: the primary key decides).
	ErrAppDocExists = errors.New("app document exists")
	// ErrAppDocNotFound reports a document the scope does not hold.
	ErrAppDocNotFound = errors.New("app document not found")
	// ErrAppWiping refuses a write while the app's key is being destroyed.
	ErrAppWiping = errors.New("app data is being deleted")
	// ErrAppKeyChanged refuses a write sealed under a key that is no longer
	// the app's (destroyed and replaced since it was read): seal again.
	ErrAppKeyChanged = errors.New("app key changed")
	// ErrAppScope refuses an app id whose data space belongs to another
	// room or tenant.
	ErrAppScope = errors.New("app belongs to another room")
	// ErrAppDocQueryUnscoped refuses a find that names no reader and is not
	// an owner's.
	ErrAppDocQueryUnscoped = errors.New("app document query names no reader")
)

// AppDocs is the app data store: memory and MySQL backends, one contract
// suite (TestContractAppDocs…).
type AppDocs interface {
	// EnsureAppKey returns the scope's key, storing k (its AppID, RoomID,
	// WrappedKey, CreatedAtMs; the tenant is the context's) when the app
	// has none. A key is only created in a live room (ErrRoomNotFound); an
	// app keyed in another room or tenant is ErrAppScope, one being
	// destroyed ErrAppWiping.
	EnsureAppKey(ctx context.Context, k AppKey) (AppKey, error)
	// GetAppKey returns the scope's key; found=false when the app has none
	// (no document was ever written, or its data was deleted). A key being
	// destroyed is ErrAppWiping.
	GetAppKey(ctx context.Context, scope AppScope) (AppKey, bool, error)
	// InsertAppDoc stores a new document sealed under wrappedKey, which
	// must still be the app's key (ErrAppKeyChanged, ErrAppWiping), at
	// version 1. An id the collection holds is ErrAppDocExists.
	InsertAppDoc(ctx context.Context, d AppDoc, wrappedKey string) error
	// GetAppDoc reads one document.
	GetAppDoc(ctx context.Context, ref AppDocRef) (AppDoc, bool, error)
	// FindAppDocs reads one page of a collection.
	FindAppDocs(ctx context.Context, q AppDocQuery) ([]AppDoc, error)
	// UpdateAppDoc is the one document write: in one transaction it locks
	// the app's key (shared) and the document (SELECT … FOR UPDATE), calls
	// fn with the wrapped key and the current document, and stores what fn
	// returns: its Public, BodySealed and UpdatedAtMs, at the current
	// version + 1. An error from fn aborts the write and is returned. fn
	// runs under the lock: it must not call the store.
	UpdateAppDoc(ctx context.Context, ref AppDocRef, fn func(wrappedKey string, cur AppDoc) (AppDoc, error)) (AppDoc, error)
	// DeleteAppDoc deletes one document after check accepts the locked
	// current one (an error from check aborts and is returned).
	DeleteAppDoc(ctx context.Context, ref AppDocRef, check func(cur AppDoc) error) error
	// WipeApp crypto-shreds the app: its key is destroyed first (every
	// sealed document is unreadable from then on, a copy in a backup
	// included), then its documents are deleted in batches and the key row
	// last. It returns the documents deleted. A later write mints a new key.
	WipeApp(ctx context.Context, scope AppScope) (int64, error)
	// CountAppDocs returns the app's usage.
	CountAppDocs(ctx context.Context, scope AppScope) (AppCounts, error)
	// RewrapAppKeys re-wraps every app's key, of every tenant, with rewrap
	// (master-key rotation: documents are not re-encrypted). It returns the
	// keys re-wrapped; a key destroyed meanwhile is skipped.
	RewrapAppKeys(ctx context.Context, rewrap func(AppKey) (string, error)) (int, error)
}

var (
	_ AppDocs = (*MemoryAppDocs)(nil)
	_ AppDocs = (*MySQLAppDocs)(nil)
)

// MemoryAppDocs is the in-memory AppDocs. A memory wipe is atomic, so it
// never shows a key being destroyed.
type MemoryAppDocs struct {
	mu    sync.Mutex
	rooms *MemoryRoom
	keys  map[string]AppKey            // app_id -> key
	docs  map[string]map[string]AppDoc // app_id -> collection\x00doc_id -> doc
}

// NewMemoryAppDocs builds the memory store over the room store whose live
// rooms may hold app keys.
func NewMemoryAppDocs(rooms *MemoryRoom) *MemoryAppDocs {
	return &MemoryAppDocs{rooms: rooms, keys: map[string]AppKey{}, docs: map[string]map[string]AppDoc{}}
}

func appDocKey(collection, docID string) string { return collection + "\x00" + docID }

func cloneAppDoc(d AppDoc) AppDoc {
	d.AuthorSealed = slices.Clone(d.AuthorSealed)
	d.BodySealed = slices.Clone(d.BodySealed)
	return d
}

// keyLocked returns the scope's key: found=false when the app has none,
// ErrAppScope when its id is another room's or tenant's.
func (s *MemoryAppDocs) keyLocked(tid string, scope AppScope) (AppKey, bool, error) {
	k, ok := s.keys[scope.AppID]
	if !ok {
		return AppKey{}, false, nil
	}
	if k.TenantID != tid || k.RoomID != scope.RoomID {
		return AppKey{}, false, ErrAppScope
	}
	return k, true, nil
}

// docLocked returns the scope's document (its app's tenant and room match).
func (s *MemoryAppDocs) docLocked(tid string, ref AppDocRef) (AppDoc, bool) {
	d, ok := s.docs[ref.AppID][appDocKey(ref.Collection, ref.DocID)]
	if !ok || d.TenantID != tid || d.RoomID != ref.RoomID {
		return AppDoc{}, false
	}
	return d, true
}

func (s *MemoryAppDocs) EnsureAppKey(ctx context.Context, k AppKey) (AppKey, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppKey{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	have, ok, err := s.keyLocked(tid, AppScope{AppID: k.AppID, RoomID: k.RoomID})
	if err != nil || ok {
		return have, err
	}
	if !s.rooms.roomLive(tid, k.RoomID) {
		return AppKey{}, ErrRoomNotFound
	}
	k.TenantID = tid
	s.keys[k.AppID] = k
	return k, nil
}

func (s *MemoryAppDocs) GetAppKey(ctx context.Context, scope AppScope) (AppKey, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppKey{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok, err := s.keyLocked(tid, scope)
	if errors.Is(err, ErrAppScope) {
		return AppKey{}, false, nil // another scope's app: none in this one
	}
	return k, ok, err
}

func (s *MemoryAppDocs) InsertAppDoc(ctx context.Context, d AppDoc, wrappedKey string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok, err := s.keyLocked(tid, AppScope{AppID: d.AppID, RoomID: d.RoomID})
	if err != nil {
		return err
	}
	if !ok || k.WrappedKey != wrappedKey {
		return ErrAppKeyChanged
	}
	if s.docs[d.AppID] == nil {
		s.docs[d.AppID] = map[string]AppDoc{}
	}
	dk := appDocKey(d.Collection, d.DocID)
	if _, taken := s.docs[d.AppID][dk]; taken {
		return ErrAppDocExists
	}
	d.TenantID, d.Version = tid, 1
	s.docs[d.AppID][dk] = cloneAppDoc(d)
	return nil
}

func (s *MemoryAppDocs) GetAppDoc(ctx context.Context, ref AppDocRef) (AppDoc, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppDoc{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docLocked(tid, ref)
	return cloneAppDoc(d), ok, nil
}

func (s *MemoryAppDocs) FindAppDocs(ctx context.Context, q AppDocQuery) ([]AppDoc, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if !q.AllDocs && q.ReaderKey == "" {
		return nil, ErrAppDocQueryUnscoped
	}
	limit := clampLimit(q.Limit, defaultAppFindLimit, wire.MaxAppFindLimit)
	s.mu.Lock()
	defer s.mu.Unlock()
	var page []AppDoc
	for _, d := range s.docs[q.AppID] {
		switch {
		case d.TenantID != tid || d.RoomID != q.RoomID || d.Collection != q.Collection:
		case !q.AllDocs && !d.Public && d.ByKey != q.ReaderKey:
		case q.By != "" && d.ByKey != q.By:
		case q.After != "" && !q.Desc && d.DocID <= q.After:
		case q.After != "" && q.Desc && d.DocID >= q.After:
		default:
			page = append(page, d)
		}
	}
	sort.Slice(page, func(i, j int) bool {
		if q.Desc {
			return page[i].DocID > page[j].DocID
		}
		return page[i].DocID < page[j].DocID
	})
	out := []AppDoc{}
	for i := 0; i < len(page) && len(out) < limit; i++ {
		out = append(out, cloneAppDoc(page[i]))
	}
	return out, nil
}

func (s *MemoryAppDocs) UpdateAppDoc(ctx context.Context, ref AppDocRef, fn func(wrappedKey string, cur AppDoc) (AppDoc, error)) (AppDoc, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppDoc{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok, err := s.keyLocked(tid, ref.AppScope)
	if err != nil {
		return AppDoc{}, err
	}
	cur, found := s.docLocked(tid, ref)
	if !ok || !found {
		return AppDoc{}, ErrAppDocNotFound
	}
	next, err := fn(k.WrappedKey, cloneAppDoc(cur))
	if err != nil {
		return AppDoc{}, err
	}
	cur.Public, cur.BodySealed, cur.UpdatedAtMs = next.Public, slices.Clone(next.BodySealed), next.UpdatedAtMs
	cur.Version++
	s.docs[ref.AppID][appDocKey(ref.Collection, ref.DocID)] = cur
	return cloneAppDoc(cur), nil
}

func (s *MemoryAppDocs) DeleteAppDoc(ctx context.Context, ref AppDocRef, check func(cur AppDoc) error) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.docLocked(tid, ref)
	if !ok {
		return ErrAppDocNotFound
	}
	if check != nil {
		if err := check(cloneAppDoc(cur)); err != nil {
			return err
		}
	}
	delete(s.docs[ref.AppID], appDocKey(ref.Collection, ref.DocID))
	return nil
}

func (s *MemoryAppDocs) WipeApp(ctx context.Context, scope AppScope) (int64, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.keyLocked(tid, scope); err != nil {
		return 0, err
	}
	delete(s.keys, scope.AppID)
	var n int64
	for dk, d := range s.docs[scope.AppID] {
		if d.TenantID == tid && d.RoomID == scope.RoomID {
			delete(s.docs[scope.AppID], dk)
			n++
		}
	}
	return n, nil
}

func (s *MemoryAppDocs) CountAppDocs(ctx context.Context, scope AppScope) (AppCounts, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppCounts{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := AppCounts{Collections: map[string]int64{}}
	for _, d := range s.docs[scope.AppID] {
		if d.TenantID == tid && d.RoomID == scope.RoomID {
			c.Docs++
			c.Bytes += int64(len(d.BodySealed))
			c.Collections[d.Collection]++
		}
	}
	return c, nil
}

func (s *MemoryAppDocs) RewrapAppKeys(_ context.Context, rewrap func(AppKey) (string, error)) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, k := range s.keys {
		w, err := rewrap(k)
		if err != nil {
			return n, err
		}
		k.WrappedKey = w
		s.keys[id] = k
		n++
	}
	return n, nil
}

// app_docs, app_keys: a deleted room's apps lose their keys and documents.
func (s *MemoryAppDocs) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, k := range s.keys {
		if k.TenantID == tenantID && k.RoomID == roomID {
			delete(s.keys, id)
		}
	}
	for id, docs := range s.docs {
		for dk, d := range docs {
			if d.TenantID == tenantID && d.RoomID == roomID {
				delete(docs, dk)
			}
		}
		if len(docs) == 0 {
			delete(s.docs, id)
		}
	}
}
