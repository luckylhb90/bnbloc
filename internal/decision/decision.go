// Package decision is the one owner of decision model calls
// (docs/prd/2026-10-17-decision-models.md): a model that answers bounded
// questions about an input with calibrated numbers (a yes/no probability, a
// choice with a probability per value, a score on a scale).
//
// A decision provider is a platform setting like a media provider: the
// shared endpoint rules are internal/modelsettings' (base URL, models, a
// sealed key, an organization's own on a host the platform allows), `kind`
// selects the wire adapter from the registry, and `auth` is "key" or
// "caller". Nothing here names a vendor or a deployment's models. Every
// request goes through a client held to the provider's host.
package decision

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/modelsettings"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// settingKey is the platform setting holding the decision provider; an
// organization's own is settingKey + "/" + its tenant id.
const settingKey = "decision.decide"

// AdapterKindOpenAIDecisions is the OpenAI Decisions wire family: POST
// {base}/decisions.
const AdapterKindOpenAIDecisions = "openai-decisions"

// How a provider is authenticated (the stored row's own "auth" field).
const (
	// AuthKey: a sealed API key the admin configured (the default).
	AuthKey = "key"
	// AuthCaller: each call forwards the caller's own bearer, only when it
	// is the acting member's own (the gateway decides).
	AuthCaller = "caller"
)

// Timeout bounds one decision call (decisions answer in about a second).
const Timeout = 60 * time.Second

// ErrProviderUnreachable: the provider did not answer or answered garbage.
var ErrProviderUnreachable = errors.New("the decision provider could not be reached")

// ProviderError is a provider's refusal: its HTTP status and its own
// message (bounded).
type ProviderError struct {
	Status  int
	Message string
}

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the decision provider answered %d", e.Status)
	}
	return fmt.Sprintf("the decision provider answered %d: %s", e.Status, e.Message)
}

// Adapter is one wire protocol family. It knows the provider's shapes only;
// the address, model, key and params arrive in Provider.
type Adapter interface {
	Kind() string
	Decide(ctx context.Context, p Provider, model string, req Request) (wire.DecisionReply, error)
}

// adapters is the registry: every decision wire family the platform can
// talk to. New families register here and nowhere else.
var adapters = map[string]Adapter{
	AdapterKindOpenAIDecisions: &openAIDecisions{},
}

// AdapterKinds are the registered adapters' kinds, sorted.
func AdapterKinds() []string {
	out := make([]string, 0, len(adapters))
	for k := range adapters {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Provider is one resolved provider: the endpoint, its models (the first
// the default), the forwarded caller bearer (auth "caller"), the adapter's
// params and the client held to the endpoint's host.
type Provider struct {
	Model  modelsettings.Model
	Models []string
	// Caller marks an auth-"caller" provider; Forward is the caller's own
	// Authorization value the gateway pinned (WithCallerAuth).
	Caller  bool
	Forward string
	Params  map[string]string
	adapter Adapter
	client  *http.Client
}

// Choose is the model to call: the asked one when the provider serves it,
// else the default (the first).
func (p Provider) Choose(asked string) string {
	asked = strings.TrimSpace(asked)
	for _, m := range p.Models {
		if m == asked {
			return asked
		}
	}
	return p.Model.Model
}

// WithCallerAuth pins the caller's own Authorization value on an
// auth-"caller" provider.
func (p Provider) WithCallerAuth(header string) Provider {
	if p.Caller {
		p.Forward = strings.TrimSpace(header)
	}
	return p
}

// setAuth authenticates one request: the caller's forwarded bearer, or the
// sealed key.
func (p Provider) setAuth(req *http.Request) {
	switch {
	case p.Caller && p.Forward != "":
		req.Header.Set("Authorization", p.Forward)
	case !p.Caller && p.Model.APIKey != "":
		req.Header.Set("Authorization", "Bearer "+p.Model.APIKey)
	}
}

// Decide asks the provider. req must have passed Validate.
func (p Provider) Decide(ctx context.Context, req Request) (wire.DecisionReply, error) {
	if p.adapter == nil {
		return wire.DecisionReply{}, ErrProviderUnreachable
	}
	if p.Caller && p.Forward == "" {
		return wire.DecisionReply{}, ErrNoCallerAuth
	}
	return p.adapter.Decide(ctx, p, p.Choose(req.Model), req)
}

// ErrNoCallerAuth: an auth-"caller" provider asked without the acting
// member's own bearer (an agent acting for someone else).
var ErrNoCallerAuth = errors.New("this decision provider bills each caller's own session, which an agent acting for someone else does not carry")

// Settings reads and writes the decision provider: the platform's row and
// an organization's own.
type Settings struct {
	Store store.PlatformSettingsStore
	Box   *sealbox.Box
	Now   func() int64
}

func (s *Settings) settings(tenant string) *modelsettings.Settings {
	return &modelsettings.Settings{Store: s.Store, Box: s.Box, Now: s.Now, Key: settingKey, Tenant: tenant}
}

// The provider row's own fields beside the shared endpoint fields.
const (
	ownKind         = "kind"
	ownModels       = "models"
	ownAuth         = "auth"
	ownParamsPrefix = "params."
)

// View is the provider as a console shows it (never the key). tenant ""
// is the platform's row.
func (s *Settings) View(ctx context.Context, tenant string) (wire.MediaProviderConfig, error) {
	v, err := s.settings(tenant).View(ctx)
	if err != nil {
		return wire.MediaProviderConfig{}, err
	}
	out := wire.MediaProviderConfig{Configurable: v.Configurable, BaseURL: v.BaseURL, Model: v.Model,
		KeySet: v.KeySet, OrgHosts: v.OrgHosts, UpdatedBy: v.UpdatedBy, UpdatedAtMs: v.UpdatedAtMs, Params: map[string]string{}}
	if out.OrgHosts == nil {
		out.OrgHosts = []string{}
	}
	for k, val := range v.Own {
		switch {
		case strings.HasPrefix(k, ownParamsPrefix):
			out.Params[strings.TrimPrefix(k, ownParamsPrefix)] = val
		case k == ownKind:
			out.Kind = val
		case k == ownModels:
			out.Models = splitList(val)
		case k == ownAuth:
			out.Auth = val
		}
	}
	if len(out.Models) == 0 && out.Model != "" {
		out.Models = []string{out.Model}
	}
	return out, nil
}

// Put stores a provider update (tenant "" is the platform's row). It uses
// the media provider update's shape: the same console form.
func (s *Settings) Put(ctx context.Context, tenant string, u wire.MediaProviderUpdate, actor string) (wire.MediaProviderConfig, error) {
	kind := strings.TrimSpace(u.Kind)
	if _, ok := adapters[kind]; !ok {
		return wire.MediaProviderConfig{}, modelsettings.Invalid("kind must be one of the registered adapters: %s", strings.Join(AdapterKinds(), ", "))
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
		if len(m) > modelsettings.MaxModel || strings.ContainsAny(m, "\r\n,") {
			return wire.MediaProviderConfig{}, modelsettings.Invalid("models must be one line of at most %d characters each, without commas", modelsettings.MaxModel)
		}
	}
	model := strings.TrimSpace(u.Model)
	if len(models) > 0 {
		model = models[0]
	}
	own := map[string]string{ownKind: kind, ownAuth: auth}
	if len(models) > 0 {
		own[ownModels] = strings.Join(models, ",")
	}
	for k, v := range u.Params {
		own[ownParamsPrefix+k] = v
	}
	if _, err := s.settings(tenant).Put(ctx, modelsettings.Update{BaseURL: u.BaseURL, Model: model,
		APIKey: u.APIKey, ClearKey: u.ClearKey, OrgHosts: u.OrgHosts, Own: own}, actor); err != nil {
		return wire.MediaProviderConfig{}, err
	}
	return s.View(ctx, tenant)
}

// Provider resolves an organization's provider: its own while set and its
// host allowed (modelsettings.Effective), else the platform's; ok false
// when none is configured.
func (s *Settings) Provider(ctx context.Context, tenant string) (Provider, bool, error) {
	m, err := s.settings("").Effective(ctx, tenant)
	if err != nil || !m.Configured() {
		return Provider{}, false, err
	}
	rowTenant := ""
	if m.Org {
		rowTenant = tenant
	}
	v, err := s.View(ctx, rowTenant)
	if err != nil {
		return Provider{}, false, err
	}
	adapter, ok := adapters[strings.TrimSpace(v.Kind)]
	if !ok {
		return Provider{}, false, fmt.Errorf("the decision provider's kind %q is not a registered adapter", v.Kind)
	}
	models := v.Models
	if len(models) == 0 {
		models = []string{m.Model}
	}
	client, err := guardedClient(m.BaseURL)
	if err != nil {
		return Provider{}, false, err
	}
	return Provider{Model: m, Models: models, Caller: v.Auth == AuthCaller, Params: v.Params, adapter: adapter, client: client}, true, nil
}

// guardedClient is an HTTP client held to base's host, following no
// redirect.
func guardedClient(base string) (*http.Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, errors.New("the provider's base URL is not an http(s) URL with a host")
	}
	return &http.Client{
		Transport:     extgov.NewNetGuard([]string{u.Host}).RoundTripper(http.DefaultTransport),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       Timeout,
	}, nil
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
