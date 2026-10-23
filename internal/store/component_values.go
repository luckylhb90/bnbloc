package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"sync"
)

// ComponentValue is one value a service component keeps in the platform
// (docs/architecture/service-components.md, the store primitive): the
// component ("<kind>/<name>"), whose it is (Scope: "members/<subject>",
// "orgs/<org>" or "shared", component.StorePath), its key and its value,
// sealed by the caller (internal/sealbox) before it is stored.
type ComponentValue struct {
	Component   string
	Scope       string
	Key         string
	Sealed      string
	UpdatedAtMs int64
}

// ComponentValueStore keeps service components' values. Reads are by the
// component that wrote them: no call reads another component's rows.
type ComponentValueStore interface {
	PutComponentValue(ctx context.Context, v ComponentValue) error
	GetComponentValue(ctx context.Context, component, scope, key string) (ComponentValue, bool, error)
	DeleteComponentValue(ctx context.Context, component, scope, key string) error
	// ListComponentValueKeys lists a scope's keys of one component, sorted.
	ListComponentValueKeys(ctx context.Context, component, scope string) ([]string, error)
	// DeleteComponentValues removes every value of a component (its
	// uninstall).
	DeleteComponentValues(ctx context.Context, component string) error
	// DeleteScopeValues removes every component's values of one scope (a
	// member who left).
	DeleteScopeValues(ctx context.Context, scope string) error
}

var (
	_ ComponentValueStore = (*MemoryComponentValues)(nil)
	_ ComponentValueStore = (*MySQLComponentValues)(nil)
)

var errComponentValueKey = errors.New("store: component, scope and key are required")

type componentValueKey struct{ component, scope, key string }

// MemoryComponentValues is the in-memory ComponentValueStore.
type MemoryComponentValues struct {
	mu   sync.Mutex
	rows map[componentValueKey]ComponentValue
}

// NewMemoryComponentValues builds an empty in-memory store.
func NewMemoryComponentValues() *MemoryComponentValues {
	return &MemoryComponentValues{rows: map[componentValueKey]ComponentValue{}}
}

func (m *MemoryComponentValues) PutComponentValue(_ context.Context, v ComponentValue) error {
	if v.Component == "" || v.Scope == "" || v.Key == "" {
		return errComponentValueKey
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[componentValueKey{v.Component, v.Scope, v.Key}] = v
	return nil
}

func (m *MemoryComponentValues) GetComponentValue(_ context.Context, component, scope, key string) (ComponentValue, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.rows[componentValueKey{component, scope, key}]
	return v, ok, nil
}

func (m *MemoryComponentValues) DeleteComponentValue(_ context.Context, component, scope, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, componentValueKey{component, scope, key})
	return nil
}

func (m *MemoryComponentValues) ListComponentValueKeys(_ context.Context, component, scope string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for k := range m.rows {
		if k.component == component && k.scope == scope {
			out = append(out, k.key)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *MemoryComponentValues) DeleteComponentValues(_ context.Context, component string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.rows {
		if k.component == component {
			delete(m.rows, k)
		}
	}
	return nil
}

func (m *MemoryComponentValues) DeleteScopeValues(_ context.Context, scope string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.rows {
		if k.scope == scope {
			delete(m.rows, k)
		}
	}
	return nil
}

// MySQLComponentValues is the MySQL ComponentValueStore (component_values).
type MySQLComponentValues struct{ DB *sql.DB }

// NewMySQLComponentValues builds the MySQL store.
func NewMySQLComponentValues(db *sql.DB) *MySQLComponentValues { return &MySQLComponentValues{DB: db} }

func (s *MySQLComponentValues) PutComponentValue(ctx context.Context, v ComponentValue) error {
	if v.Component == "" || v.Scope == "" || v.Key == "" {
		return errComponentValueKey
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO component_values (component, scope, value_key, sealed, updated_at_ms)
  VALUES (?, ?, ?, ?, ?)
  ON DUPLICATE KEY UPDATE sealed = VALUES(sealed), updated_at_ms = VALUES(updated_at_ms)`,
		v.Component, v.Scope, v.Key, v.Sealed, v.UpdatedAtMs)
	return err
}

func (s *MySQLComponentValues) GetComponentValue(ctx context.Context, component, scope, key string) (ComponentValue, bool, error) {
	v := ComponentValue{Component: component, Scope: scope, Key: key}
	err := s.DB.QueryRowContext(ctx, `SELECT sealed, updated_at_ms FROM component_values
  WHERE component = ? AND scope = ? AND value_key = ?`, component, scope, key).Scan(&v.Sealed, &v.UpdatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return ComponentValue{}, false, nil
	}
	if err != nil {
		return ComponentValue{}, false, err
	}
	return v, true, nil
}

func (s *MySQLComponentValues) DeleteComponentValue(ctx context.Context, component, scope, key string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM component_values WHERE component = ? AND scope = ? AND value_key = ?`, component, scope, key)
	return err
}

func (s *MySQLComponentValues) ListComponentValueKeys(ctx context.Context, component, scope string) ([]string, error) {
	out, err := queryAll(ctx, s.DB, `SELECT value_key FROM component_values WHERE component = ? AND scope = ? ORDER BY value_key`,
		func(r rowScanner) (string, error) {
			var k string
			err := r.Scan(&k)
			return k, err
		}, component, scope)
	if out == nil {
		out = []string{}
	}
	return out, err
}

func (s *MySQLComponentValues) DeleteComponentValues(ctx context.Context, component string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM component_values WHERE component = ?`, component)
	return err
}

func (s *MySQLComponentValues) DeleteScopeValues(ctx context.Context, scope string) error {
	if strings.TrimSpace(scope) == "" {
		return errComponentValueKey
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM component_values WHERE scope = ?`, scope)
	return err
}
