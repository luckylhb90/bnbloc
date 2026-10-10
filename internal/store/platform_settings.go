package store

import (
	"context"
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

// PlatformSettingsStore persists platform-wide settings. Implementations must
// be safe for concurrent use.
type PlatformSettingsStore interface {
	PutPlatformSetting(ctx context.Context, setting PlatformSetting) error
	GetPlatformSetting(ctx context.Context, key string) (PlatformSetting, bool, error)
	ListPlatformSettings(ctx context.Context) ([]PlatformSetting, error)
	DeletePlatformSetting(ctx context.Context, key string) error
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

func (s *MemoryPlatformSettings) DeletePlatformSetting(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.settings, key)
	return nil
}
