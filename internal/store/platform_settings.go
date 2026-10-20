package store

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// PlatformSetting is one instance-wide key/value setting. It is intentionally
// not tenant-scoped: platform settings control the whole self-hosted instance.
type PlatformSetting struct {
	Key         string
	Value       string
	UpdatedAtMs int64
	UpdatedBy   string
}

// ErrPlatformSettingChanged refuses a compare-and-set write whose expected
// row is no longer the stored one: a concurrent writer won. The caller
// re-reads and retries (UpdatePlatformSetting).
var ErrPlatformSettingChanged = errors.New("store: platform setting changed concurrently")

// PlatformSettingsStore persists platform-wide settings. Implementations must
// be safe for concurrent use.
type PlatformSettingsStore interface {
	PutPlatformSetting(ctx context.Context, setting PlatformSetting) error
	GetPlatformSetting(ctx context.Context, key string) (PlatformSetting, bool, error)
	// ListPlatformSettings returns every setting by key ([] when none).
	ListPlatformSettings(ctx context.Context) ([]PlatformSetting, error)
	DeletePlatformSetting(ctx context.Context, key string) error
	// SwapPlatformSetting writes next only while the stored row of next.Key
	// is still prev (value, time and author exactly as read; prev nil: only
	// while the key is unset), else ErrPlatformSettingChanged. It is the
	// write of a read-modify-write: UpdatePlatformSetting.
	SwapPlatformSetting(ctx context.Context, prev *PlatformSetting, next PlatformSetting) error
}

// maxSettingSwaps bounds UpdatePlatformSetting's retries: every lost round
// is another writer's win, so N concurrent writers all land within N rounds.
const maxSettingSwaps = 32

// UpdatePlatformSetting is the one read-modify-write of a platform setting:
// it reads key, lets change compute the next value from the current one
// (found=false: unset), and writes it with SwapPlatformSetting, re-reading
// and re-applying change when a concurrent writer won, so no concurrent
// update is lost. change errors abort. It returns the written value.
func UpdatePlatformSetting(ctx context.Context, st PlatformSettingsStore, key, actor string, atMs int64, change func(cur string, found bool) (string, error)) (string, error) {
	for range maxSettingSwaps {
		row, found, err := st.GetPlatformSetting(ctx, key)
		if err != nil {
			return "", err
		}
		next, err := change(row.Value, found)
		if err != nil {
			return "", err
		}
		var prev *PlatformSetting
		if found {
			prev = &row
		}
		err = st.SwapPlatformSetting(ctx, prev, PlatformSetting{Key: key, Value: next, UpdatedAtMs: atMs, UpdatedBy: actor})
		if errors.Is(err, ErrPlatformSettingChanged) {
			continue
		}
		return next, err
	}
	return "", ErrPlatformSettingChanged
}

// MovePlatformSetting moves key to value unless the stored value is
// already the same (same(stored, value)). Of concurrent movers to one value
// exactly one moves it: it gets moved=true and the value it replaced (""
// when the key was unset); the others see the value already there. It is
// the once-only record of a change (a platform event,
// services/gateway/platform_events.go).
func MovePlatformSetting(ctx context.Context, st PlatformSettingsStore, key, value, actor string, atMs int64, same func(stored, value string) bool) (prev string, moved bool, err error) {
	for range maxSettingSwaps {
		row, found, err := st.GetPlatformSetting(ctx, key)
		if err != nil {
			return "", false, err
		}
		if found && same(row.Value, value) {
			return row.Value, false, nil
		}
		var cur *PlatformSetting
		if found {
			cur = &row
		}
		err = st.SwapPlatformSetting(ctx, cur, PlatformSetting{Key: key, Value: value, UpdatedAtMs: atMs, UpdatedBy: actor})
		if errors.Is(err, ErrPlatformSettingChanged) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		return cur.valueOrEmpty(), true, nil
	}
	return "", false, ErrPlatformSettingChanged
}

// valueOrEmpty is a read row's value ("" for none).
func (p *PlatformSetting) valueOrEmpty() string {
	if p == nil {
		return ""
	}
	return p.Value
}

// MemoryPlatformSettings is an in-memory PlatformSettingsStore for dev/tests.
type MemoryPlatformSettings struct {
	mu       sync.Mutex
	settings map[string]PlatformSetting
}

// NewMemoryPlatformSettings constructs an empty in-memory platform settings store.
func NewMemoryPlatformSettings() *MemoryPlatformSettings {
	return &MemoryPlatformSettings{settings: map[string]PlatformSetting{}}
}

var _ PlatformSettingsStore = (*MemoryPlatformSettings)(nil)

func (s *MemoryPlatformSettings) PutPlatformSetting(_ context.Context, setting PlatformSetting) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[setting.Key] = setting
	return nil
}

func (s *MemoryPlatformSettings) GetPlatformSetting(_ context.Context, key string) (PlatformSetting, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	setting, ok := s.settings[key]
	return setting, ok, nil
}

func (s *MemoryPlatformSettings) ListPlatformSettings(_ context.Context) ([]PlatformSetting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PlatformSetting, 0, len(s.settings))
	for _, setting := range s.settings {
		out = append(out, setting)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *MemoryPlatformSettings) SwapPlatformSetting(_ context.Context, prev *PlatformSetting, next PlatformSetting) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, found := s.settings[next.Key]
	if found != (prev != nil) || (found && cur != *prev) {
		return ErrPlatformSettingChanged
	}
	s.settings[next.Key] = next
	return nil
}

func (s *MemoryPlatformSettings) DeletePlatformSetting(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.settings, key)
	return nil
}
