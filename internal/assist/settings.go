package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The assist model's settings: one platform setting, its key sealed
// (internal/sealbox, the settings key as additional data) and never
// returned. Without the secret store key the model cannot be configured
// (a key is never stored in clear), and a stored one is not opened.

// settingKey is the platform setting holding the assist model.
const settingKey = "assist.model"

// Bounds of the settings.
const (
	maxBaseURL = 2048
	maxModel   = 200
	maxAPIKey  = 4 << 10
)

// ErrInvalidSettings wraps a refused update (its message says what to fix).
var ErrInvalidSettings = errors.New("invalid assist model")

// ErrSettingsOff: the gateway has no secret store key.
var ErrSettingsOff = errors.New("the assist model cannot be configured: the gateway has no " + sealbox.KeyEnv)

// Settings reads and writes the assist model.
type Settings struct {
	Store store.PlatformSettingsStore
	// Box seals the key; nil = the model cannot be configured.
	Box *sealbox.Box
	Now func() int64
}

type storedModel struct {
	BaseURL   string `json:"baseUrl"`
	Model     string `json:"model"`
	SealedKey string `json:"sealedKey,omitempty"`
}

// Configurable reports whether the model can be stored and read.
func (s *Settings) Configurable() bool { return s != nil && s.Store != nil && s.Box != nil }

// read returns the stored row (ok false when unset).
func (s *Settings) read(ctx context.Context) (storedModel, store.PlatformSetting, bool, error) {
	row, ok, err := s.Store.GetPlatformSetting(ctx, settingKey)
	if err != nil || !ok {
		return storedModel{}, store.PlatformSetting{}, false, err
	}
	var sm storedModel
	if err := json.Unmarshal([]byte(row.Value), &sm); err != nil {
		return storedModel{}, store.PlatformSetting{}, false, fmt.Errorf("decode the assist model: %w", err)
	}
	return sm, row, true, nil
}

// Model returns the stored model with its key opened. A model is
// unconfigured (Configured false) when unset or the settings are off.
func (s *Settings) Model(ctx context.Context) (Model, error) {
	if !s.Configurable() {
		return Model{}, nil
	}
	sm, _, ok, err := s.read(ctx)
	if err != nil || !ok {
		return Model{}, err
	}
	m := Model{BaseURL: sm.BaseURL, Model: sm.Model}
	if sm.SealedKey != "" {
		key, err := s.Box.Open(sm.SealedKey, settingKey)
		if err != nil {
			return Model{}, fmt.Errorf("open the assist model's key: %w", err)
		}
		m.APIKey = string(key)
	}
	return m, nil
}

// View is the model as the platform console shows it: never the key.
func (s *Settings) View(ctx context.Context) (wire.AssistModelConfig, error) {
	out := wire.AssistModelConfig{Configurable: s.Configurable()}
	if !out.Configurable {
		return out, nil
	}
	sm, row, ok, err := s.read(ctx)
	if err != nil || !ok {
		return out, err
	}
	out.BaseURL, out.Model, out.KeySet = sm.BaseURL, sm.Model, sm.SealedKey != ""
	out.UpdatedBy, out.UpdatedAtMs = row.UpdatedBy, row.UpdatedAtMs
	return out, nil
}

// Put stores an update: an empty key keeps the stored one, ClearKey
// removes it, an empty base URL turns the model off (and forgets it).
func (s *Settings) Put(ctx context.Context, u wire.AssistModelUpdate, actor string) (wire.AssistModelConfig, error) {
	if !s.Configurable() {
		return wire.AssistModelConfig{}, ErrSettingsOff
	}
	base := strings.TrimRight(strings.TrimSpace(u.BaseURL), "/")
	if base == "" {
		if err := s.Store.DeletePlatformSetting(ctx, settingKey); err != nil {
			return wire.AssistModelConfig{}, err
		}
		return s.View(ctx)
	}
	next := storedModel{BaseURL: base, Model: strings.TrimSpace(u.Model)}
	if err := validate(next.BaseURL, next.Model, u.APIKey); err != nil {
		return wire.AssistModelConfig{}, err
	}
	prev, _, _, err := s.read(ctx)
	if err != nil {
		return wire.AssistModelConfig{}, err
	}
	switch key := strings.TrimSpace(u.APIKey); {
	case key != "":
		if next.SealedKey, err = s.Box.Seal([]byte(key), settingKey); err != nil {
			return wire.AssistModelConfig{}, err
		}
	case !u.ClearKey:
		next.SealedKey = prev.SealedKey
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return wire.AssistModelConfig{}, err
	}
	var now int64
	if s.Now != nil {
		now = s.Now()
	}
	if err := s.Store.PutPlatformSetting(ctx, store.PlatformSetting{Key: settingKey, Value: string(raw), UpdatedAtMs: now, UpdatedBy: actor}); err != nil {
		return wire.AssistModelConfig{}, err
	}
	return s.View(ctx)
}

// validate checks a model's settings: an http(s) base URL with a host and
// no credentials, query or fragment; a one-line model name; a bounded key.
func validate(base, model, key string) error {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(base) > maxBaseURL {
		return fmt.Errorf("%w: baseUrl must be an http(s) URL with a host, such as https://llm.example.com/v1 (no credentials, query or fragment)", ErrInvalidSettings)
	}
	if model == "" || len(model) > maxModel || strings.ContainsAny(model, "\r\n") {
		return fmt.Errorf("%w: model must be one line of at most %d characters", ErrInvalidSettings, maxModel)
	}
	if len(key) > maxAPIKey || !utf8.ValidString(key) || strings.ContainsAny(key, "\r\n") {
		return fmt.Errorf("%w: apiKey must be one line of at most %d bytes", ErrInvalidSettings, maxAPIKey)
	}
	return nil
}
