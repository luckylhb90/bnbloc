package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The assist model's settings: the platform's model and an
// organization's own, each one platform setting, its key sealed
// (internal/sealbox, the settings key as additional data, so a row moved
// to another organization does not open) and never returned. Without the
// secret store key no model can be configured (a key is never stored in
// clear), and a stored one is not opened.
//
// The platform's model carries OrgHosts: the hosts an organization's own
// model may name (none by default), so an org admin cannot point the
// gateway at an arbitrary address. An organization's model replaces the
// platform's for its members while its host stays allowed (Effective).

// settingKey is the platform setting holding the platform's model; an
// organization's is settingKey + "/" + its tenant id.
const settingKey = "assist.model"

// Bounds of the settings.
const (
	maxBaseURL  = 2048
	maxModel    = 200
	maxAPIKey   = 4 << 10
	maxOrgHosts = 20
)

// hostRE is a host[:port] an organization's model may name (lower-case).
var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

// ErrInvalidSettings wraps a refused update (its message says what to fix).
var ErrInvalidSettings = errors.New("invalid assist model")

// ErrSettingsOff: the gateway has no secret store key.
var ErrSettingsOff = errors.New("the assist model cannot be configured: the gateway has no " + sealbox.KeyEnv)

// Settings reads and writes one assist model: the platform's (Tenant "")
// or an organization's.
type Settings struct {
	Store store.PlatformSettingsStore
	// Box seals the key; nil = the model cannot be configured.
	Box *sealbox.Box
	Now func() int64
	// Tenant names the organization whose model this is ("" = the
	// platform's).
	Tenant string
}

type storedModel struct {
	BaseURL   string `json:"baseUrl"`
	Model     string `json:"model"`
	SealedKey string `json:"sealedKey,omitempty"`
	// OrgHosts: the platform's model only.
	OrgHosts []string `json:"orgHosts,omitempty"`
}

// key is the setting's key (also the sealed key's additional data).
func (s *Settings) key() string {
	if s.Tenant == "" {
		return settingKey
	}
	return settingKey + "/" + s.Tenant
}

// Platform is the platform's model settings beside s.
func (s *Settings) Platform() *Settings {
	return &Settings{Store: s.Store, Box: s.Box, Now: s.Now}
}

// Org is an organization's model settings beside s.
func (s *Settings) Org(tenant string) *Settings {
	return &Settings{Store: s.Store, Box: s.Box, Now: s.Now, Tenant: tenant}
}

// Configurable reports whether the model can be stored and read.
func (s *Settings) Configurable() bool { return s != nil && s.Store != nil && s.Box != nil }

// read returns the stored row (ok false when unset).
func (s *Settings) read(ctx context.Context) (storedModel, store.PlatformSetting, bool, error) {
	row, ok, err := s.Store.GetPlatformSetting(ctx, s.key())
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
		key, err := s.Box.Open(sm.SealedKey, s.key())
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
	out.BaseURL, out.Model, out.KeySet, out.OrgHosts = sm.BaseURL, sm.Model, sm.SealedKey != "", sm.OrgHosts
	out.UpdatedBy, out.UpdatedAtMs = row.UpdatedBy, row.UpdatedAtMs
	return out, nil
}

// Put stores an update: an empty key keeps the stored one, ClearKey
// removes it, an empty base URL turns the model off (and forgets it; an
// organization's members go back to the platform's). OrgHosts is the
// platform's to set; an organization's base URL must name one of them.
func (s *Settings) Put(ctx context.Context, u wire.AssistModelUpdate, actor string) (wire.AssistModelConfig, error) {
	if !s.Configurable() {
		return wire.AssistModelConfig{}, ErrSettingsOff
	}
	hosts, err := s.orgHosts(u.OrgHosts)
	if err != nil {
		return wire.AssistModelConfig{}, err
	}
	base := strings.TrimRight(strings.TrimSpace(u.BaseURL), "/")
	if base == "" {
		if err := s.Store.DeletePlatformSetting(ctx, s.key()); err != nil {
			return wire.AssistModelConfig{}, err
		}
		return s.View(ctx)
	}
	next := storedModel{BaseURL: base, Model: strings.TrimSpace(u.Model), OrgHosts: hosts}
	if err := validate(next.BaseURL, next.Model, u.APIKey); err != nil {
		return wire.AssistModelConfig{}, err
	}
	if s.Tenant != "" {
		allowed, err := s.AllowedHosts(ctx)
		if err != nil {
			return wire.AssistModelConfig{}, err
		}
		if !hostAllowed(next.BaseURL, allowed) {
			if len(allowed) == 0 {
				return wire.AssistModelConfig{}, fmt.Errorf("%w: the platform allows no host for an organization's own model", ErrInvalidSettings)
			}
			return wire.AssistModelConfig{}, fmt.Errorf("%w: baseUrl's host must be one the platform allows: %s", ErrInvalidSettings, strings.Join(allowed, ", "))
		}
	}
	prev, _, _, err := s.read(ctx)
	if err != nil {
		return wire.AssistModelConfig{}, err
	}
	switch key := strings.TrimSpace(u.APIKey); {
	case key != "":
		if next.SealedKey, err = s.Box.Seal([]byte(key), s.key()); err != nil {
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
	if err := s.Store.PutPlatformSetting(ctx, store.PlatformSetting{Key: s.key(), Value: string(raw), UpdatedAtMs: now, UpdatedBy: actor}); err != nil {
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

// orgHosts checks an update's OrgHosts: the platform's only, each a
// host[:port] (lower-cased), unique, bounded.
func (s *Settings) orgHosts(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if s.Tenant != "" {
		return nil, fmt.Errorf("%w: orgHosts is the platform's to set", ErrInvalidSettings)
	}
	if len(in) > maxOrgHosts {
		return nil, fmt.Errorf("%w: at most %d orgHosts", ErrInvalidSettings, maxOrgHosts)
	}
	var out []string
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || slices.Contains(out, h) {
			continue
		}
		if !hostRE.MatchString(h) {
			return nil, fmt.Errorf("%w: orgHosts %q is not a host[:port] such as llm.example.com", ErrInvalidSettings, h)
		}
		out = append(out, h)
	}
	return out, nil
}

// AllowedHosts are the hosts an organization's own model may name: the
// platform model's OrgHosts (none when the platform has no model).
func (s *Settings) AllowedHosts(ctx context.Context) ([]string, error) {
	sm, _, ok, err := s.Platform().read(ctx)
	if err != nil || !ok {
		return nil, err
	}
	return sm.OrgHosts, nil
}

// hostAllowed reports a base URL whose host[:port] is one of allowed
// (case-insensitive, exact).
func hostAllowed(base string, allowed []string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	return slices.Contains(allowed, strings.ToLower(u.Host))
}

// Effective is the model an organization's members run on: its own while
// set and its host still allowed (Model.Org), else the platform's.
func (s *Settings) Effective(ctx context.Context, tenant string) (Model, error) {
	if !s.Configurable() {
		return Model{}, nil
	}
	if tenant != "" {
		own, err := s.Org(tenant).Model(ctx)
		if err != nil {
			return Model{}, err
		}
		if own.Configured() {
			allowed, err := s.AllowedHosts(ctx)
			if err != nil {
				return Model{}, err
			}
			if hostAllowed(own.BaseURL, allowed) {
				own.Org = true
				return own, nil
			}
		}
	}
	return s.Platform().Model(ctx)
}
