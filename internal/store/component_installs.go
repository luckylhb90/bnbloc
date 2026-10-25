package store

import (
	"context"
	"sort"
	"sync"
)

// ComponentInstall is one member's opt-in install of a distributed component
// (an extension kind+name, e.g. core/acme). Auto-rollout components need no
// row: the member surface derives them from the extension records themselves.
// Subject keys the row because the component registry is instance-global.
type ComponentInstall struct {
	Subject       string
	Kind          string
	Name          string
	InstalledAtMs int64
}

// ComponentInstallStore persists per-member opt-in installs of distributed
// components. Implementations must be safe for concurrent use.
type ComponentInstallStore interface {
	// ListComponentInstalls returns every component subject has installed.
	ListComponentInstalls(ctx context.Context, subject string) ([]ComponentInstall, error)
	// CountComponentInstalls returns, for every name of the given kind, how
	// many distinct subjects installed it (adoption stats).
	CountComponentInstalls(ctx context.Context, kind string) (map[string]int, error)
	// PutComponentInstall records subject's install of kind/name (idempotent).
	PutComponentInstall(ctx context.Context, subject, kind, name string, atMs int64) error
	// DeleteComponentInstall removes subject's install of kind/name (a missing
	// row is a no-op).
	DeleteComponentInstall(ctx context.Context, subject, kind, name string) error
}

// MemoryComponentInstall is the in-memory ComponentInstallStore (dev / tests).
type MemoryComponentInstall struct {
	mu   sync.Mutex
	rows map[string]ComponentInstall
}

// NewMemoryComponentInstall constructs an in-memory component install store.
func NewMemoryComponentInstall() *MemoryComponentInstall {
	return &MemoryComponentInstall{rows: map[string]ComponentInstall{}}
}

var _ ComponentInstallStore = (*MemoryComponentInstall)(nil)

func componentInstallKey(subject, kind, name string) string {
	return subject + "\x00" + kind + "\x00" + name
}

func (s *MemoryComponentInstall) ListComponentInstalls(_ context.Context, subject string) ([]ComponentInstall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ComponentInstall{}
	for _, r := range s.rows {
		if r.Subject == subject {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *MemoryComponentInstall) CountComponentInstalls(_ context.Context, kind string) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, r := range s.rows {
		if r.Kind == kind {
			out[r.Name]++
		}
	}
	return out, nil
}

func (s *MemoryComponentInstall) PutComponentInstall(_ context.Context, subject, kind, name string, atMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := componentInstallKey(subject, kind, name)
	if _, exists := s.rows[key]; exists {
		return nil
	}
	s.rows[key] = ComponentInstall{
		Subject: subject, Kind: kind, Name: name, InstalledAtMs: atMs,
	}
	return nil
}

func (s *MemoryComponentInstall) DeleteComponentInstall(_ context.Context, subject, kind, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, componentInstallKey(subject, kind, name))
	return nil
}

// MySQLComponentInstall implements ComponentInstallStore over database/sql.
type MySQLComponentInstall struct{ DB *DB }

// NewMySQLComponentInstall constructs a MySQL-backed component install store.
func NewMySQLComponentInstall(db *DB) *MySQLComponentInstall {
	return &MySQLComponentInstall{DB: db}
}

var _ ComponentInstallStore = (*MySQLComponentInstall)(nil)

func (s *MySQLComponentInstall) ListComponentInstalls(ctx context.Context, subject string) ([]ComponentInstall, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT subject, kind, name, installed_at_ms FROM component_installs WHERE subject = ? ORDER BY kind, name`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ComponentInstall{}
	for rows.Next() {
		var r ComponentInstall
		if err := rows.Scan(&r.Subject, &r.Kind, &r.Name, &r.InstalledAtMs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *MySQLComponentInstall) CountComponentInstalls(ctx context.Context, kind string) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT name, COUNT(DISTINCT subject) FROM component_installs WHERE kind = ? GROUP BY name`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

func (s *MySQLComponentInstall) PutComponentInstall(ctx context.Context, subject, kind, name string, atMs int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO component_installs
  (subject, kind, name, installed_at_ms) VALUES (?, ?, ?, ?)
  ON DUPLICATE KEY UPDATE installed_at_ms = installed_at_ms`,
		subject, kind, name, atMs)
	return err
}

func (s *MySQLComponentInstall) DeleteComponentInstall(ctx context.Context, subject, kind, name string) error {
	_, err := s.DB.ExecContext(ctx,
		`DELETE FROM component_installs WHERE subject = ? AND kind = ? AND name = ?`, subject, kind, name)
	return err
}
