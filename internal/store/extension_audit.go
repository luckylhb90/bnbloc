package store

import (
	"context"
	"database/sql"
	"sync"
)

// ExtAuditRecord captures one governance-relevant extension event: a lifecycle
// transition (install/enable/disable/uninstall) or a sensitive drive (an auth
// login, a webhook delivery). It is a separate audit trail from the shared-agent
// AuditRecord, whose Action enum is fixed — do not overload it.
type ExtAuditRecord struct {
	Kind     string // extension kind (core|auth|webhook|…)
	Name     string
	Action   string // install|enable|disable|uninstall|drive
	Actor    string // subject who triggered a lifecycle change ("" for a drive)
	KeyID    string // signing key id at install ("" when unsigned/dev)
	TenantID string
	Outcome  string // allow|deny|delivered|failed|…
	Detail   string
	AtMs     int64
}

// ExtensionAuditStore persists extension audit records (append-only). They
// are read through AuditLog (audit_log.go). Implementations must be safe for
// concurrent use.
type ExtensionAuditStore interface {
	AppendExtAudit(ctx context.Context, rec ExtAuditRecord) error
}

// MemoryExtensionAudit is the in-memory audit store (dev / tests).
type MemoryExtensionAudit struct {
	mu   sync.Mutex
	recs []ExtAuditRecord
}

// NewMemoryExtensionAudit constructs an in-memory extension audit store.
func NewMemoryExtensionAudit() *MemoryExtensionAudit { return &MemoryExtensionAudit{} }

var _ ExtensionAuditStore = (*MemoryExtensionAudit)(nil)

func (s *MemoryExtensionAudit) AppendExtAudit(_ context.Context, rec ExtAuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, rec)
	return nil
}

// MySQLExtensionAudit implements ExtensionAuditStore over database/sql.
type MySQLExtensionAudit struct{ DB *sql.DB }

// NewMySQLExtensionAudit constructs a MySQL-backed extension audit store.
func NewMySQLExtensionAudit(db *sql.DB) *MySQLExtensionAudit { return &MySQLExtensionAudit{DB: db} }

var _ ExtensionAuditStore = (*MySQLExtensionAudit)(nil)

func (s *MySQLExtensionAudit) AppendExtAudit(ctx context.Context, rec ExtAuditRecord) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO extension_audit
  (kind, name, action, actor, key_id, tenant_id, outcome, detail, at_ms)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Kind, rec.Name, rec.Action, rec.Actor, rec.KeyID, rec.TenantID, rec.Outcome, rec.Detail, rec.AtMs)
	return err
}
