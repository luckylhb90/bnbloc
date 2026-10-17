// Package media is the one owner of the media model calls
// (docs/prd/2026-10-16-voice-and-image-generation.md): speech-to-text and
// image generation through admin-configured providers. Nothing here names
// a vendor or hardcodes a deployment's model catalog: a provider is a
// platform setting (base URL, model, sealed key) whose `kind` selects a
// wire protocol adapter from the registry, and whose `params` are the
// adapter's own knobs. A provider with another wire shape is a new adapter
// file and one registry entry — no route, setting or UI change.
//
// The shared endpoint-setting rules (base URL, model, sealed key, the
// platform/organization split) are internal/modelsettings'; this package
// adds the adapter registry and the two calls. Every outbound request goes
// through the guarded client of the stored provider (extgov.NetGuard: only
// the configured host, no redirects, no link-local or metadata address).
package media

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/modelsettings"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Capability is one media ability a provider serves.
type Capability string

const (
	// CapTranscribe: speech to text.
	CapTranscribe Capability = "transcribe"
	// CapImage: text to image.
	CapImage Capability = "image"
)

// Capabilities is the capability list, in a stable order.
var Capabilities = []Capability{CapTranscribe, CapImage}

// settingKey is the platform setting holding a capability's provider; an
// organization's own is settingKey + "/" + its tenant id (M6).
const settingKey = "media."

// AdapterKindOpenAICompatible is the `kind` of the OpenAI-compatible wire
// family: POST {base}/audio/transcriptions (multipart) and POST
// {base}/images/generations (JSON).
const AdapterKindOpenAICompatible = "openai-compatible"

// How a provider is authenticated (the stored row's own "auth" field).
const (
	// AuthKey: a sealed API key the admin configured (the default).
	AuthKey = "key"
	// AuthCaller: each call forwards the caller's own session token
	// (Authorization) — a proxy that bills the caller, like the platform's
	// own LLM gateway; nothing to seal.
	AuthCaller = "caller"
)

// ErrProviderUnreachable: the provider did not answer or answered garbage.
var ErrProviderUnreachable = errors.New("the media provider could not be reached")

// ProviderError is a provider's refusal: its HTTP status and its own
// message (bounded), so the caller can surface why it refused.
type ProviderError struct {
	Status  int
	Message string
}

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the media provider answered %d", e.Status)
	}
	return fmt.Sprintf("the media provider answered %d: %s", e.Status, e.Message)
}

// Adapter is one wire protocol family: how a capability of its kind is
// called. An adapter knows the provider's shapes only; the provider's
// address, model, key and params arrive in Provider.
type Adapter interface {
	// Kind names the adapter (the settings' `kind` selects it).
	Kind() string
	// Serves reports whether the adapter can call the capability.
	Serves(cap Capability) bool
	// Transcribe turns audio into text (Serves(CapTranscribe) adapters).
	Transcribe(ctx context.Context, p Provider, audio []byte, mimeType, filename string) (string, error)
	// Generate draws (or, with Input set, edits) images in one provider call
	// (Serves(CapImage) adapters): Count images, 1…MaxMediaImages.
	Generate(ctx context.Context, p Provider, req ImageRequest) ([]Image, error)
}

// adapters is the registry: every wire protocol family the platform can
// talk to. New families register here and nowhere else.
var adapters = map[string]Adapter{
	AdapterKindOpenAICompatible: &openAICompatible{},
}

// AdapterKinds are the registered adapters' kinds, sorted (the settings UI
// offers them).
func AdapterKinds() []string {
	out := make([]string, 0, len(adapters))
	for k := range adapters {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// Provider is one resolved provider: the endpoint, its models (the first
// the default), the opened key (or the caller's own token to forward,
// Auth "caller"), the adapter's own params, and the HTTP client held to
// the endpoint's host. The gateway resolves it from the settings per call;
// nothing else builds one.
type Provider struct {
	Model  modelsettings.Model
	Models []string
	// AuthForward names the header a "caller"-auth provider is called with:
	// the caller's own Authorization header (the gateway pins it per call).
	AuthForward string
	Params      map[string]string
	adapter     Adapter
	client      *http.Client
}

// Choose returns the model to call: the asked one when the provider serves
// it, else the default (the first).
func (p Provider) Choose(asked string) string {
	asked = strings.TrimSpace(asked)
	if asked == "" {
		return p.Model.Model
	}
	for _, m := range p.Models {
		if m == asked {
			return asked
		}
	}
	return p.Model.Model
}

// ImageRequest is one generation ask: Count images drawn from Prompt (an
// edit of Input when set), in one provider call. Model names one of the
// provider's Models (the first is the default).
type ImageRequest struct {
	Prompt string
	Model  string
	Aspect string // wire.MediaImageAspect*
	Count  int
	// Input, when set, is the image to change (an edit): its bytes and MIME
	// type. The caller checks the caller may read it.
	Input *ImageInput
	// Mask, with Input, is the edit's mask (EditMask): a PNG the size of
	// Input, transparent where the image may change.
	Mask []byte
	// References are further images the prompt names (a style, a logo),
	// sent beside Input as more image parts; an edit needs Input.
	References []ImageInput
}

// ImageInput is the image an edit changes, or a reference.
type ImageInput struct {
	Bytes    []byte
	MimeType string
}

// Image is one generated image: the bytes, sniffed MIME type and the
// provider's revised prompt (data the provider wrote).
type Image struct {
	Bytes         []byte
	MimeType      string
	RevisedPrompt string
}

// Settings reads and writes the providers: the platform's rows, and an
// organization's own (the assist model's pattern: the platform's row carries
// the hosts an organization's own provider may name; an organization's
// provider replaces the platform's for its members while its host stays
// allowed).
type Settings struct {
	Store store.PlatformSettingsStore
	// Box seals the key; nil = the provider cannot be configured.
	Box *sealbox.Box
	Now func() int64
}

// settings is the capability's modelsettings view (the platform's row, or
// tenant's own).
func (s *Settings) settings(cap Capability, tenant string) *modelsettings.Settings {
	return &modelsettings.Settings{Store: s.Store, Box: s.Box, Now: s.Now, Key: settingKey + string(cap), Tenant: tenant}
}

// Configurable reports whether a provider can be stored and read.
func (s *Settings) Configurable() bool {
	return s != nil && s.Store != nil && s.Box != nil
}

// View is the provider as the console shows it: never the key (the
// platform's row carries orgHosts; an organization's row shows none).
func (s *Settings) View(ctx context.Context, cap Capability, tenant string) (wire.MediaProviderConfig, error) {
	v, err := s.settings(cap, tenant).View(ctx)
	if err != nil {
		return wire.MediaProviderConfig{}, err
	}
	out := wire.MediaProviderConfig{Configurable: v.Configurable, BaseURL: v.BaseURL, Model: v.Model,
		KeySet: v.KeySet, OrgHosts: v.OrgHosts, UpdatedBy: v.UpdatedBy, UpdatedAtMs: v.UpdatedAtMs, Params: map[string]string{}}
	if out.OrgHosts == nil {
		out.OrgHosts = []string{}
	}
	for k, val := range v.Own {
		if strings.HasPrefix(k, ownParamsPrefix) {
			out.Params[strings.TrimPrefix(k, ownParamsPrefix)] = val
		}
		if k == ownKind {
			out.Kind = val
		}
		if k == ownModels {
			out.Models = splitList(val)
		}
		if k == ownAuth {
			out.Auth = val
		}
	}
	if len(out.Models) == 0 && out.Model != "" {
		out.Models = []string{out.Model}
	}
	return out, err
}

// splitList splits a comma-separated own field.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ownKind, ownModels, ownAuth and ownParamsPrefix are the provider row's
// own fields beside the shared endpoint fields (modelsettings.Own).
const (
	ownKind         = "kind"
	ownModels       = "models"
	ownAuth         = "auth"
	ownParamsPrefix = "params."
)

// Put stores a provider update (the platform's row, or an organization's
// own on a host the platform allows). kind must name a registered adapter
// that serves the capability.
func (s *Settings) Put(ctx context.Context, cap Capability, tenant string, u wire.MediaProviderUpdate, actor string) (wire.MediaProviderConfig, error) {
	kind := strings.TrimSpace(u.Kind)
	adapter, ok := adapters[kind]
	if !ok {
		return wire.MediaProviderConfig{}, modelsettings.Invalid("kind must be one of the registered adapters: %s", strings.Join(AdapterKinds(), ", "))
	}
	if !adapter.Serves(cap) {
		return wire.MediaProviderConfig{}, modelsettings.Invalid("the %s adapter cannot serve %s", kind, cap)
	}
	if err := modelsettings.ValidateOwn(u.Params); err != nil {
		return wire.MediaProviderConfig{}, modelsettings.Invalid("%s", err.Error())
	}
	auth := strings.TrimSpace(u.Auth)
	if auth == "" {
		auth = AuthKey
	}
	if auth != AuthKey && auth != AuthCaller {
		return wire.MediaProviderConfig{}, modelsettings.Invalid("auth must be %q or %q", AuthKey, AuthCaller)
	}
	models := u.Models
	if len(models) == 0 && strings.TrimSpace(u.Model) != "" {
		models = []string{strings.TrimSpace(u.Model)}
	}
	for _, m := range models {
		if len(m) > modelsettings.MaxModel || strings.ContainsAny(m, "\r\n") {
			return wire.MediaProviderConfig{}, modelsettings.Invalid("models must be one line of at most %d characters each", modelsettings.MaxModel)
		}
	}
	own := map[string]string{ownKind: kind, ownAuth: auth}
	if len(models) > 0 {
		own[ownModels] = strings.Join(models, ",")
	}
	for k, v := range u.Params {
		own[ownParamsPrefix+k] = v
	}
	_, err := s.settings(cap, tenant).Put(ctx, modelsettings.Update{BaseURL: u.BaseURL, Model: u.Model,
		APIKey: u.APIKey, ClearKey: u.ClearKey, OrgHosts: u.OrgHosts, Own: own}, actor)
	if err != nil {
		return wire.MediaProviderConfig{}, err
	}
	return s.View(ctx, cap, tenant)
}

// Provider resolves the capability's provider for an organization: its own
// while set and its host allowed (modelsettings.Effective), else the
// platform's, with its key opened and its adapter selected; ok false when
// none is configured. It fails when the stored kind names no registered
// adapter (an adapter was removed).
func (s *Settings) Provider(ctx context.Context, cap Capability, tenant string) (Provider, bool, error) {
	set := s.settings(cap, "")
	m, err := set.Effective(ctx, tenant)
	if err != nil || !m.Configured() {
		return Provider{}, false, err
	}
	kind, err := set.Own(ctx, ownKind)
	if err != nil {
		return Provider{}, false, err
	}
	adapter, ok := adapters[strings.TrimSpace(kind)]
	if !ok {
		return Provider{}, false, fmt.Errorf("the %s provider's kind %q is not a registered adapter", cap, kind)
	}
	// The params arrive with the kind in the row's own fields; read them
	// through the one reader of the row. An organization's own provider's
	// params are its row's own (the kind travels in the platform row and
	// every org row).
	params := map[string]string{}
	row := set
	if m.Org {
		row = s.settings(cap, tenant)
		if k, kerr := row.Own(ctx, ownKind); kerr == nil && strings.TrimSpace(k) != "" {
			kind = k
		}
	}
	v, err := row.View(ctx)
	if err != nil {
		return Provider{}, false, err
	}
	models := []string{}
	auth := ""
	for k, val := range v.Own {
		if strings.HasPrefix(k, ownParamsPrefix) {
			params[strings.TrimPrefix(k, ownParamsPrefix)] = val
		}
		if k == ownModels {
			models = splitList(val)
		}
		if k == ownAuth {
			auth = val
		}
	}
	if len(models) == 0 {
		models = []string{m.Model}
	}
	base, err := guardedClient(m)
	if err != nil {
		return Provider{}, false, err
	}
	forward := ""
	if auth == AuthCaller {
		forward = AuthCaller
	}
	return Provider{Model: m, Models: models, AuthForward: forward, Params: params, adapter: adapter, client: base}, true, nil
}

// guardedClient is an HTTP client held to m's host (never follows a
// redirect: a provider never redirects a media call, and following one
// could leave the guarded host).
func guardedClient(m modelsettings.Model) (*http.Client, error) {
	host, err := hostOf(m.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("the provider's base URL is invalid: %w", err)
	}
	return &http.Client{
		Transport:     extgov.NewNetGuard([]string{host}).RoundTripper(http.DefaultTransport),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       Timeout,
	}, nil
}

// Timeout bounds one media call.
const Timeout = 120 * time.Second

// Transcribe turns audio into text through the capability's provider.
func (p Provider) Transcribe(ctx context.Context, audio []byte, mimeType, filename string) (string, error) {
	if p.adapter == nil || !p.adapter.Serves(CapTranscribe) {
		return "", ErrProviderUnreachable
	}
	return p.adapter.Transcribe(ctx, p, audio, mimeType, filename)
}

// WithCallerAuth returns p with the caller's Authorization header pinned
// (an auth-"caller" provider): the gateway calls this per request.
func (p Provider) WithCallerAuth(header string) Provider {
	if p.AuthForward != "" {
		p.AuthForward = header
	}
	return p
}

// Generate draws (or edits) images through the capability's provider.
func (p Provider) Generate(ctx context.Context, req ImageRequest) ([]Image, error) {
	if p.adapter == nil || !p.adapter.Serves(CapImage) {
		return nil, ErrProviderUnreachable
	}
	return p.adapter.Generate(ctx, p, req)
}
