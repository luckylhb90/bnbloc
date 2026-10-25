package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MySQLAppDocs is the MySQL AppDocs over app_docs and app_keys.
type MySQLAppDocs struct{ DB *DB }

// NewMySQLAppDocs builds the MySQL store.
func NewMySQLAppDocs(db *DB) *MySQLAppDocs { return &MySQLAppDocs{DB: db} }

const appDocCols = `app_id, collection, doc_id, tenant_id, room_id, by_key, public, author_sealed, version, body_sealed, created_at_ms, updated_at_ms`

// appKeyDestroyed is the SET clause that destroys a key in place: the row
// stays (refusing writes, ErrAppWiping) until its documents are deleted.
const appKeyDestroyed = `wrapped_key = ''`

// appScopeArgs binds `app_id = ? AND tenant_id = ? AND room_id = ?`, the
// scope every app query names (spelled in each query, so the tenant filter
// is visible to TestTenantTablesFilterTenant).
func appScopeArgs(tid string, s AppScope) []any { return []any{s.AppID, tid, s.RoomID} }

func scanAppDoc(r rowScanner) (AppDoc, error) {
	var d AppDoc
	err := r.Scan(&d.AppID, &d.Collection, &d.DocID, &d.TenantID, &d.RoomID, &d.ByKey, &d.Public, &d.AuthorSealed, &d.Version,
		&d.BodySealed, &d.CreatedAtMs, &d.UpdatedAtMs)
	return d, err
}

func scanAppKey(r rowScanner) (AppKey, error) {
	var k AppKey
	err := r.Scan(&k.AppID, &k.TenantID, &k.RoomID, &k.WrappedKey, &k.CreatedAtMs)
	return k, err
}

// lockedAppKey reads the scope's key row inside tx, locked shared (a wipe's
// destroy waits for the writes in flight). found=false when there is none;
// a destroyed key is ErrAppWiping.
func lockedAppKey(ctx context.Context, tx *sql.Tx, tid string, s AppScope) (string, bool, error) {
	var wrapped string
	err := tx.QueryRowContext(ctx, `SELECT wrapped_key FROM app_keys WHERE app_id = ? AND tenant_id = ? AND room_id = ? FOR SHARE`, appScopeArgs(tid, s)...).Scan(&wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if wrapped == "" {
		return "", false, ErrAppWiping
	}
	return wrapped, true, nil
}

func (s *MySQLAppDocs) EnsureAppKey(ctx context.Context, k AppKey) (AppKey, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppKey{}, err
	}
	// Insert-only (the app id carries no tenant), and only from the
	// tenant's live room: the read of rooms serializes with DeleteRoom.
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO app_keys (app_id, tenant_id, room_id, wrapped_key, created_at_ms)
		 SELECT ?, tenant_id, room_id, ?, ? FROM rooms WHERE tenant_id = ? AND room_id = ? AND `+sqlNotDeleted+`
		 ON DUPLICATE KEY UPDATE app_id = app_id`,
		k.AppID, k.WrappedKey, k.CreatedAtMs, tid, k.RoomID); err != nil {
		return AppKey{}, err
	}
	have, err := scanAppKey(s.DB.QueryRowContext(ctx, `SELECT app_id, tenant_id, room_id, wrapped_key, created_at_ms FROM app_keys WHERE app_id = ?`, k.AppID))
	if errors.Is(err, sql.ErrNoRows) {
		return AppKey{}, ErrRoomNotFound
	}
	if err != nil {
		return AppKey{}, err
	}
	switch {
	case have.TenantID != tid || have.RoomID != k.RoomID:
		return AppKey{}, ErrAppScope
	case have.WrappedKey == "":
		return AppKey{}, ErrAppWiping
	}
	return have, nil
}

func (s *MySQLAppDocs) GetAppKey(ctx context.Context, scope AppScope) (AppKey, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppKey{}, false, err
	}
	k, err := scanAppKey(s.DB.QueryRowContext(ctx, `SELECT app_id, tenant_id, room_id, wrapped_key, created_at_ms FROM app_keys WHERE app_id = ? AND tenant_id = ? AND room_id = ?`,
		appScopeArgs(tid, scope)...))
	if errors.Is(err, sql.ErrNoRows) {
		return AppKey{}, false, nil
	}
	if err != nil {
		return AppKey{}, false, err
	}
	if k.WrappedKey == "" {
		return AppKey{}, false, ErrAppWiping
	}
	return k, true, nil
}

func (s *MySQLAppDocs) InsertAppDoc(ctx context.Context, d AppDoc, wrappedKey string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	have, ok, err := lockedAppKey(ctx, tx, tid, AppScope{AppID: d.AppID, RoomID: d.RoomID})
	if err != nil {
		return err
	}
	if !ok || have != wrappedKey {
		return ErrAppKeyChanged
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_docs (`+appDocCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		d.AppID, d.Collection, d.DocID, tid, d.RoomID, d.ByKey, d.Public, d.AuthorSealed, d.BodySealed, d.CreatedAtMs, d.UpdatedAtMs); err != nil {
		return dupAs(err, ErrAppDocExists)
	}
	return tx.Commit()
}

func (s *MySQLAppDocs) GetAppDoc(ctx context.Context, ref AppDocRef) (AppDoc, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppDoc{}, false, err
	}
	d, err := scanAppDoc(s.DB.QueryRowContext(ctx, `SELECT `+appDocCols+` FROM app_docs WHERE app_id = ? AND tenant_id = ? AND room_id = ? AND collection = ? AND doc_id = ?`,
		append(appScopeArgs(tid, ref.AppScope), ref.Collection, ref.DocID)...))
	if errors.Is(err, sql.ErrNoRows) {
		return AppDoc{}, false, nil
	}
	return d, err == nil, err
}

func (s *MySQLAppDocs) FindAppDocs(ctx context.Context, q AppDocQuery) ([]AppDoc, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if !q.AllDocs && q.ReaderKey == "" {
		return nil, ErrAppDocQueryUnscoped
	}
	query := `SELECT ` + appDocCols + ` FROM app_docs WHERE app_id = ? AND tenant_id = ? AND room_id = ? AND collection = ?`
	args := append(appScopeArgs(tid, q.AppScope), q.Collection)
	if !q.AllDocs {
		query += ` AND (public = 1 OR by_key = ?)`
		args = append(args, q.ReaderKey)
	}
	if q.By != "" {
		query += ` AND by_key = ?`
		args = append(args, q.By)
	}
	order := ` ORDER BY doc_id`
	if q.After != "" {
		if q.Desc {
			query += ` AND doc_id < ?`
		} else {
			query += ` AND doc_id > ?`
		}
		args = append(args, q.After)
	}
	if q.Desc {
		order += ` DESC`
	}
	query += order + ` LIMIT ?`
	args = append(args, clampLimit(q.Limit, defaultAppFindLimit, wire.MaxAppFindLimit))
	return queryAll(ctx, s.DB, query, scanAppDoc, args...)
}

func (s *MySQLAppDocs) UpdateAppDoc(ctx context.Context, ref AppDocRef, fn func(wrappedKey string, cur AppDoc) (AppDoc, error)) (AppDoc, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppDoc{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AppDoc{}, err
	}
	defer func() { _ = tx.Rollback() }()
	wrapped, ok, err := lockedAppKey(ctx, tx, tid, ref.AppScope)
	if err != nil {
		return AppDoc{}, err
	}
	if !ok {
		return AppDoc{}, ErrAppDocNotFound
	}
	where := `app_id = ? AND tenant_id = ? AND room_id = ? AND collection = ? AND doc_id = ?`
	args := append(appScopeArgs(tid, ref.AppScope), ref.Collection, ref.DocID)
	cur, err := scanAppDoc(tx.QueryRowContext(ctx, `SELECT `+appDocCols+` FROM app_docs WHERE `+where+` FOR UPDATE`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return AppDoc{}, ErrAppDocNotFound
	}
	if err != nil {
		return AppDoc{}, err
	}
	next, err := fn(wrapped, cur)
	if err != nil {
		return AppDoc{}, err
	}
	cur.Public, cur.BodySealed, cur.UpdatedAtMs = next.Public, slices.Clone(next.BodySealed), next.UpdatedAtMs
	cur.Version++
	if _, err := tx.ExecContext(ctx, `UPDATE app_docs SET public = ?, body_sealed = ?, version = ?, updated_at_ms = ? WHERE `+where,
		append([]any{cur.Public, cur.BodySealed, cur.Version, cur.UpdatedAtMs}, args...)...); err != nil {
		return AppDoc{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppDoc{}, err
	}
	return cur, nil
}

func (s *MySQLAppDocs) DeleteAppDoc(ctx context.Context, ref AppDocRef, check func(cur AppDoc) error) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	where := `app_id = ? AND tenant_id = ? AND room_id = ? AND collection = ? AND doc_id = ?`
	args := append(appScopeArgs(tid, ref.AppScope), ref.Collection, ref.DocID)
	cur, err := scanAppDoc(tx.QueryRowContext(ctx, `SELECT `+appDocCols+` FROM app_docs WHERE `+where+` FOR UPDATE`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAppDocNotFound
	}
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(cur); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_docs WHERE `+where, args...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *MySQLAppDocs) WipeApp(ctx context.Context, scope AppScope) (int64, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	args := appScopeArgs(tid, scope)
	// Destroy the key first (it waits for the writes holding it): from
	// here on no document of the app opens, and no write lands.
	if _, err := s.DB.ExecContext(ctx, `UPDATE app_keys SET `+appKeyDestroyed+` WHERE app_id = ? AND tenant_id = ? AND room_id = ?`, args...); err != nil {
		return 0, err
	}
	const where = `app_id = ? AND tenant_id = ? AND room_id = ?`
	n, err := deleteInBatches(ctx, s.DB, "app_docs", where, args)
	if err != nil {
		return n, err
	}
	if _, err := deleteInBatches(ctx, s.DB, "app_files", where, args); err != nil {
		return n, err
	}
	_, err = deleteInBatches(ctx, s.DB, "app_keys", where, args)
	return n, err
}

// deleteInBatches deletes the rows of table matching where, appShredBatch
// rows per statement, and returns how many it deleted. It is the second
// half of a crypto-shred (WipeApp, DeleteRoom's batched tables): the rows
// are already unreadable, so no transaction spans the batches.
func deleteInBatches(ctx context.Context, db sqlConn, table, where string, args []any) (int64, error) {
	var total int64
	for {
		res, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+where+` LIMIT ?`, append(slices.Clone(args), appShredBatch)...)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(appShredBatch) {
			return total, nil
		}
	}
}

func (s *MySQLAppDocs) CountAppDocs(ctx context.Context, scope AppScope) (AppCounts, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppCounts{}, err
	}
	type row struct {
		collection string
		docs       int64
		bytes      int64
	}
	rows, err := queryAll(ctx, s.DB, `SELECT collection, COUNT(*), COALESCE(SUM(LENGTH(body_sealed)), 0) FROM app_docs WHERE app_id = ? AND tenant_id = ? AND room_id = ? GROUP BY collection`,
		func(r rowScanner) (row, error) {
			var v row
			err := r.Scan(&v.collection, &v.docs, &v.bytes)
			return v, err
		}, appScopeArgs(tid, scope)...)
	if err != nil {
		return AppCounts{}, err
	}
	c := AppCounts{Collections: map[string]int64{}}
	for _, r := range rows {
		c.Docs += r.docs
		c.Bytes += r.bytes
		c.Collections[r.collection] = r.docs
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size_bytes), 0) FROM app_files WHERE app_id = ? AND tenant_id = ? AND room_id = ?`,
		appScopeArgs(tid, scope)...).Scan(&c.Files, &c.FileBytes); err != nil {
		return AppCounts{}, err
	}
	return c, nil
}

const appFileCols = `app_id, file_id, tenant_id, room_id, by_key, public, name_sealed, author_sealed, content_type, size_bytes, blob_key, created_at_ms`

func scanAppFile(r rowScanner) (AppFile, error) {
	var f AppFile
	err := r.Scan(&f.AppID, &f.FileID, &f.TenantID, &f.RoomID, &f.ByKey, &f.Public, &f.NameSealed, &f.AuthorSealed, &f.ContentType, &f.Size, &f.BlobKey, &f.CreatedAtMs)
	return f, err
}

func (s *MySQLAppDocs) InsertAppFile(ctx context.Context, f AppFile, wrappedKey string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	have, ok, err := lockedAppKey(ctx, tx, tid, AppScope{AppID: f.AppID, RoomID: f.RoomID})
	if err != nil {
		return err
	}
	if !ok || have != wrappedKey {
		return ErrAppKeyChanged
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_files (`+appFileCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.AppID, f.FileID, tid, f.RoomID, f.ByKey, f.Public, f.NameSealed, f.AuthorSealed, f.ContentType, f.Size, f.BlobKey, f.CreatedAtMs); err != nil {
		return dupAs(err, ErrAppDocExists)
	}
	return tx.Commit()
}

func (s *MySQLAppDocs) GetAppFile(ctx context.Context, ref AppFileRef) (AppFile, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AppFile{}, false, err
	}
	f, err := scanAppFile(s.DB.QueryRowContext(ctx, `SELECT `+appFileCols+` FROM app_files WHERE app_id = ? AND tenant_id = ? AND room_id = ? AND file_id = ?`,
		append(appScopeArgs(tid, ref.AppScope), ref.FileID)...))
	if errors.Is(err, sql.ErrNoRows) {
		return AppFile{}, false, nil
	}
	return f, err == nil, err
}

// RewrapAppKeys walks every live key in app_id order, a page at a time, and
// swaps each wrapped key only if it is unchanged (a key destroyed or
// re-wrapped meanwhile is skipped).
func (s *MySQLAppDocs) RewrapAppKeys(ctx context.Context, rewrap func(AppKey) (string, error)) (int, error) {
	// cross-tenant: a master-key rotation re-wraps the key of every app of
	// every tenant; each key's own tenant is part of what it is bound to.
	const page = 500
	n, after := 0, ""
	for {
		keys, err := queryAll(ctx, s.DB, `SELECT app_id, tenant_id, room_id, wrapped_key, created_at_ms FROM app_keys
		 WHERE app_id > ? AND wrapped_key <> '' ORDER BY app_id LIMIT ?`, scanAppKey, after, page)
		if err != nil {
			return n, err
		}
		for _, k := range keys {
			w, err := rewrap(k)
			if err != nil {
				return n, err
			}
			res, err := s.DB.ExecContext(ctx, `UPDATE app_keys SET wrapped_key = ? WHERE app_id = ? AND wrapped_key = ?`, w, k.AppID, k.WrappedKey)
			if err != nil {
				return n, err
			}
			if m, err := res.RowsAffected(); err != nil {
				return n, err
			} else if m > 0 {
				n++
			}
			after = k.AppID
		}
		if len(keys) < page {
			return n, nil
		}
	}
}

func (s *MySQLAppDocs) DeleteAppFile(ctx context.Context, ref AppFileRef, check func(cur AppFile) error) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	where := `app_id = ? AND tenant_id = ? AND room_id = ? AND file_id = ?`
	args := append(appScopeArgs(tid, ref.AppScope), ref.FileID)
	cur, err := scanAppFile(tx.QueryRowContext(ctx, `SELECT `+appFileCols+` FROM app_files WHERE `+where+` FOR UPDATE`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAppDocNotFound
	}
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(cur); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_files WHERE `+where, args...); err != nil {
		return err
	}
	return tx.Commit()
}
