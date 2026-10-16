// Package modelsettings is the one owner of the rules every
// admin-configured model endpoint shares (docs/prd/
// 2026-10-16-voice-and-image-generation.md §2): a base URL, a model name
// and a sealed API key in one platform setting, with the platform's row
// carrying the hosts an organization's own endpoint may name.
//
// It was extracted from internal/assist/settings.go when media (speech to
// text, image generation) grew the same shape; assist.Settings delegates
// to it (one rule, one owner). Nothing here names a vendor: what changes
// per deployment is configuration, validated here, reached by the caller.
package modelsettings

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
)

// Bounds of one endpoint setting.
const (
	MaxBaseURL  = 2048
	MaxModel    = 200
	MaxAPIKey   = 4 << 10
	MaxOrgHosts = 20
)

// hostRE is a host[:port] an organization's endpoint may name (lower-case).
var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

// ErrSettingsOff: the gateway has no secret store key, so nothing can be
// configured (a key is never stored in clear).
var ErrSettingsOff = errors.New("model settings cannot be configured: the gateway has no " + sealbox.KeyEnv)

// InvalidError is a refused update: its message says what to fix.
type InvalidError struct{ msg string }

func (e InvalidError) Error() string { return e.msg }

// Invalid wraps a refused update's reason.
func Invalid(format string, args ...any) error {
	return InvalidError{msg: fmt.Sprintf(format, args...)}
}

// invalid wraps a refused update's reason.
func invalid(format string, args ...any) error {
	return Invalid(format, args...)
}

// Model is one admin-configured model endpoint: an OpenAI-compatible-style
// base URL (the ".../v1" its paths hang under), the model name and the API
// key ("" for a proxy that needs none).
type Model struct {
	BaseURL string
	Model   string
	APIKey  string
	// Org marks an organization's own model (Effective).
	Org bool
}

// Configured reports an endpoint calls can run on.
func (m Model) Configured() bool { return m.BaseURL != "" && m.Model != "" }

// Settings reads and writes one model endpoint: the platform's (Tenant "")
// or an organization's, under one platform setting key. The key is sealed
// with the setting key as additional data, so a row moved to another
// organization does not open, and is never returned.
type Settings struct {
	Store store.PlatformSettingsStore
	// Box seals the key; nil = nothing can be configured.
	Box *sealbox.Box
	Now func() int64
	// Tenant names the organization whose endpoint this is ("" = the
	// platform's).
	Tenant string
	// Key is the platform setting holding the platform's endpoint; an
	// organization's is Key + "/" + its tenant id.
	Key string
	// What holds the endpoint's own fields (the JSON of a stored row) and
	// what an update carries: BaseURL, Model and OrgHosts field names are
	// shared; everything else is the setting's own.
	// Extra is the endpoint's own fields beyond the shared ones (a media
	// provider's kind and params), carried through unchanged. A key is
	// bounded by ValidateExtra.
	Extra func() map[string]string
}

// row is the whole stored JSON: shared fields beside the setting's own.
type row struct {
	BaseURL   string         `json:"baseUrl"`
	Model     string         `json:"model"`
	OrgHosts  []string       `json:"orgHosts,omitempty"`
	SealedKey string         `json:"sealedKey,omitempty"`
	// Own holds the setting's own fields (media.kind, media.params.…):
	// carried through unchanged, never interpreted here.
	Own map[string]any `json:"own,omitempty"`
}

// setting is s's platform setting key (also the sealed key's additional data).
func (s *Settings) setting() string {
	if s.Tenant == "" {
		return s.Key
	}
	return s.Key + "/" + s.Tenant
}

// Platform is the platform's settings beside s.
func (s *Settings) Platform() *Settings {
	return &Settings{Store: s.Store, Box: s.Box, Now: s.Now, Key: s.Key, Extra: s.Extra}
}

// Org is an organization's settings beside s.
func (s *Settings) Org(tenant string) *Settings {
	return &Settings{Store: s.Store, Box: s.Box, Now: s.Now, Key: s.Key, Tenant: tenant, Extra: s.Extra}
}

// Configurable reports whether an endpoint can be stored and read.
func (s *Settings) Configurable() bool { return s != nil && s.Store != nil && s.Box != nil }

// read returns the stored row (ok false when unset).
func (s *Settings) read(ctx context.Context) (row, store.PlatformSetting, bool, error) {
	storedRow, ok, err := s.Store.GetPlatformSetting(ctx, s.setting())
	if err != nil || !ok {
		return row{}, store.PlatformSetting{}, false, err
	}
	var r row
	if err := json.Unmarshal([]byte(storedRow.Value), &r); err != nil {
		return row{}, store.PlatformSetting{}, false, fmt.Errorf("decode the model settings: %w", err)
	}
	return r, storedRow, true, nil
}

// Model returns the stored endpoint with its key opened. An endpoint is
// unconfigured (Configured false) when unset or the settings are off.
func (s *Settings) Model(ctx context.Context) (Model, error) {
	if !s.Configurable() {
		return Model{}, nil
	}
	r, _, ok, err := s.read(ctx)
	if err != nil || !ok {
		return Model{}, err
	}
	m := Model{BaseURL: r.BaseURL, Model: r.Model}
	if r.SealedKey != "" {
		key, err := s.Box.Open(r.SealedKey, s.setting())
		if err != nil {
			return Model{}, fmt.Errorf("open the model's key: %w", err)
		}
		m.APIKey = string(key)
	}
	return m, nil
}

// Own returns the endpoint's own stored fields ("" when unset): what
// s.Extra names the setting as holding.
func (s *Settings) Own(ctx context.Context, name string) (string, error) {
	if !s.Configurable() {
		return "", nil
	}
	r, _, ok, err := s.read(ctx)
	if err != nil || !ok {
		return "", err
	}
	raw, ok := r.Own[name]
	if !ok {
		return "", nil
	}
	switch v := raw.(type) {
	case string:
		return v, nil
	case nil:
		return "", nil
	default:
		rawJSON, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(rawJSON), nil
	}
}

// View is the endpoint as the console shows it: never the key.
func (s *Settings) View(ctx context.Context) (View, error) {
	out := View{Configurable: s.Configurable()}
	if !out.Configurable {
		return out, nil
	}
	r, storedRow, ok, err := s.read(ctx)
	if err != nil || !ok {
		return out, err
	}
	out.BaseURL, out.Model, out.KeySet, out.OrgHosts = r.BaseURL, r.Model, r.SealedKey != "", r.OrgHosts
	out.UpdatedBy, out.UpdatedAtMs = storedRow.UpdatedBy, storedRow.UpdatedAtMs
	for name, v := range r.Own {
		if out.Own == nil {
			out.Own = map[string]string{}
		}
		switch typed := v.(type) {
		case string:
			out.Own[name] = typed
		case nil:
			out.Own[name] = ""
		default:
			rawJSON, err := json.Marshal(typed)
			if err != nil {
				return out, err
			}
			out.Own[name] = string(rawJSON)
		}
	}
	return out, nil
}

// View is an endpoint as the console shows it: never the key, only whether
// one is set, and the endpoint's own fields.
type View struct {
	Configurable bool
	BaseURL      string
	Model        string
	KeySet       bool
	OrgHosts     []string
	Own          map[string]string
	UpdatedBy    string
	UpdatedAtMs  int64
}

// Update is a Put body: an empty APIKey keeps the stored one, ClearKey
// removes it, an empty BaseURL turns the endpoint off (and forgets it).
// OrgHosts is the platform's to set; an organization's base URL must name
// one of them.
type Update struct {
	BaseURL  string
	Model    string
	APIKey   string
	ClearKey bool
	OrgHosts []string
	// Own is the endpoint's own fields to carry through unchanged.
	Own map[string]string
}

// Put stores an update (see Update). orgHosts is validated only for the
// platform's settings.
func (s *Settings) Put(ctx context.Context, u Update, actor string) (View, error) {
	if !s.Configurable() {
		return View{}, ErrSettingsOff
	}
	hosts, err := s.orgHosts(u.OrgHosts)
	if err != nil {
		return View{}, err
	}
	base := strings.TrimRight(strings.TrimSpace(u.BaseURL), "/")
	if base == "" {
		if err := s.Store.DeletePlatformSetting(ctx, s.setting()); err != nil {
			return View{}, err
		}
		return s.View(ctx)
	}
	if err := Validate(base, u.Model, u.APIKey); err != nil {
		return View{}, err
	}
	if s.Tenant != "" {
		allowed, err := s.AllowedHosts(ctx)
		if err != nil {
			return View{}, err
		}
		if !HostAllowed(base, allowed) {
			if len(allowed) == 0 {
				return View{}, invalid("the platform allows no host for an organization's own model")
			}
			return View{}, invalid("baseUrl's host must be one the platform allows: %s", strings.Join(allowed, ", "))
		}
	}
	prev, _, _, err := s.read(ctx)
	if err != nil {
		return View{}, err
	}
	next := row{BaseURL: base, Model: strings.TrimSpace(u.Model), OrgHosts: hosts}
	for k, v := range u.Own {
		if next.Own == nil {
			next.Own = map[string]any{}
		}
		next.Own[k] = v
	}
	switch key := strings.TrimSpace(u.APIKey); {
	case key != "":
		if next.SealedKey, err = s.Box.Seal([]byte(key), s.setting()); err != nil {
			return View{}, err
		}
	case !u.ClearKey:
		next.SealedKey = prev.SealedKey
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return View{}, err
	}
	var now int64
	if s.Now != nil {
		now = s.Now()
	}
	if err := s.Store.PutPlatformSetting(ctx, store.PlatformSetting{Key: s.setting(), Value: string(raw), UpdatedAtMs: now, UpdatedBy: actor}); err != nil {
		return View{}, err
	}
	return s.View(ctx)
}

// Validate checks a base URL, a model name and an API key: an http(s) URL
// with a host and no credentials, query or fragment; a one-line model; a
// bounded one-line key.
func Validate(base, model, key string) error {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(base) > MaxBaseURL {
		return invalid("baseUrl must be an http(s) URL with a host, such as https://llm.example.com/v1 (no credentials, query or fragment)")
	}
	if model == "" || len(model) > MaxModel || strings.ContainsAny(model, "\r\n") {
		return invalid("model must be one line of at most %d characters", MaxModel)
	}
	if len(key) > MaxAPIKey || !utf8.ValidString(key) || strings.ContainsAny(key, "\r\n") {
		return invalid("apiKey must be one line of at most %d bytes", MaxAPIKey)
	}
	return nil
}

// ValidateOwn bounds an endpoint's own fields: at most 16, keys of at most
// 64 characters, values of at most 512.
func ValidateOwn(own map[string]string) error {
	if len(own) > 16 {
		return invalid("at most 16 own fields")
	}
	for k, v := range own {
		if k == "" || len(k) > 64 || strings.ContainsAny(k, "\r\n") {
			return invalid("an own field name must be at most 64 characters, one line")
		}
		if len(v) > 512 || strings.ContainsAny(v, "\r\n") {
			return invalid("own field %q must be at most 512 bytes, one line", k)
		}
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
		return nil, invalid("orgHosts is the platform's to set")
	}
	if len(in) > MaxOrgHosts {
		return nil, invalid("at most %d orgHosts", MaxOrgHosts)
	}
	var out []string
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || slices.Contains(out, h) {
			continue
		}
		if !hostRE.MatchString(h) {
			return nil, invalid("orgHosts %q is not a host[:port] such as llm.example.com", h)
		}
		out = append(out, h)
	}
	return out, nil
}

// AllowedHosts are the hosts an organization's own endpoint may name: the
// platform setting's OrgHosts (none when the platform has none).
func (s *Settings) AllowedHosts(ctx context.Context) ([]string, error) {
	r, _, ok, err := s.Platform().read(ctx)
	if err != nil || !ok {
		return nil, err
	}
	return r.OrgHosts, nil
}

// HostAllowed reports a base URL whose host[:port] is one of allowed
// (case-insensitive, exact).
func HostAllowed(base string, allowed []string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	return slices.Contains(allowed, strings.ToLower(u.Host))
}

// Effective is the endpoint an organization's members run on: its own
// while set and its host still allowed (Model.Org), else the platform's.
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
			if HostAllowed(own.BaseURL, allowed) {
				own.Org = true
				return own, nil
			}
		}
	}
	return s.Platform().Model(ctx)
}
