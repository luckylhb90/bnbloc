package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// secretKeyPrefix keys a notifier component's signing secret in platform
// settings ("notify.secret/<component>"). It is generated server-side when
// the component is installed, never part of the signed package, never
// returned by a list, and deleted with the component.
const secretKeyPrefix = "notify.secret/"

// SecretStore reads and writes notifier signing secrets, with a short-TTL
// read cache for the delivery worker (a rotation in another process is seen
// within secretTTL; through this store at once).
type SecretStore struct {
	Settings store.PlatformSettingsStore
	Now      func() time.Time

	mu    sync.Mutex
	cache map[string]cachedSecret
}

type cachedSecret struct {
	secret string
	found  bool
	at     time.Time
}

const secretTTL = 5 * time.Second

func (s *SecretStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Get returns a notifier's signing secret (found=false when none is stored).
func (s *SecretStore) Get(ctx context.Context, name string) (string, bool, error) {
	if s == nil || s.Settings == nil {
		return "", false, nil
	}
	now := s.now()
	s.mu.Lock()
	if c, ok := s.cache[name]; ok && now.Sub(c.at) < secretTTL {
		s.mu.Unlock()
		return c.secret, c.found, nil
	}
	s.mu.Unlock()
	row, ok, err := s.Settings.GetPlatformSetting(ctx, secretKeyPrefix+name)
	if err != nil {
		return "", false, err
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]cachedSecret{}
	}
	s.cache[name] = cachedSecret{secret: row.Value, found: ok && row.Value != "", at: now}
	s.mu.Unlock()
	return row.Value, ok && row.Value != "", nil
}

// Ensure returns the stored secret, generating and storing one when none
// exists (created=true: the caller shows it once).
func (s *SecretStore) Ensure(ctx context.Context, name, actor string) (string, bool, error) {
	if cur, ok, err := s.Get(ctx, name); err != nil || ok {
		return cur, false, err
	}
	secret, err := s.Rotate(ctx, name, actor)
	return secret, err == nil, err
}

// Rotate stores a freshly generated secret and returns it.
func (s *SecretStore) Rotate(ctx context.Context, name, actor string) (string, error) {
	secret := NewSecret()
	if err := s.Settings.PutPlatformSetting(ctx, store.PlatformSetting{
		Key: secretKeyPrefix + name, Value: secret, UpdatedAtMs: s.now().UnixMilli(), UpdatedBy: actor,
	}); err != nil {
		return "", err
	}
	s.forget(name)
	return secret, nil
}

// Delete removes a notifier's secret (uninstall).
func (s *SecretStore) Delete(ctx context.Context, name string) error {
	if err := s.Settings.DeletePlatformSetting(ctx, secretKeyPrefix+name); err != nil {
		return err
	}
	s.forget(name)
	return nil
}

func (s *SecretStore) forget(name string) {
	s.mu.Lock()
	delete(s.cache, name)
	s.mu.Unlock()
}

// NewSecret generates a signing secret (32 random bytes).
func NewSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "whsec_" + hex.EncodeToString(b)
}
