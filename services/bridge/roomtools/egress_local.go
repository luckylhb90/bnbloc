package roomtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/component"
)

// Local egress providers (component/egress_provider.go,
// docs/prd/2026-10-27-model-credentials.md §3.3): a program on this computer
// answers the same two questions an organization's egress service does, over
// loopback. The bridge names one "local:<name>" among the services a session
// knows, so the claim, the card and the exchange are the service path's own;
// only the asker (loopback, with the attested ProviderTurn) and the answer's
// relay mode differ.

// LocalPrefix starts the service name of a local provider.
const LocalPrefix = "local:"

// localName is a provider's folder name.
var localName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

const (
	maxProviderFileBytes = 16 << 10
	// providerAsk bounds one question to a provider (never the send it decides).
	providerAsk = 30 * time.Second
	// maxProviderAnswer bounds a provider's answer to a question.
	maxProviderAnswer = 1 << 20
)

// LocalProvider is a provider on this computer, read from its descriptor.
type LocalProvider struct {
	Name  string
	url   *url.URL
	token string
}

// Service is the provider's name among a session's services.
func (p *LocalProvider) Service() string { return LocalPrefix + p.Name }

// ProviderDrop is a descriptor that did not pass, and why.
type ProviderDrop struct{ Name, Reason string }

func (d ProviderDrop) String() string { return d.Name + ": " + d.Reason }

// ReadLocalProviders reads every provider's descriptor under dir
// (<dir>/<name>/provider.json). The providers are untrusted: a descriptor
// that is not a plain small file of the contract with a loopback http
// address and a port is dropped, and at most component.MaxEgressProviders
// are taken (by name).
func ReadLocalProviders(dir string) (out []*LocalProvider, dropped []ProviderDrop) {
	if dir == "" {
		return nil, nil
	}
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, e := range list {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if !localName.MatchString(name) {
			dropped = append(dropped, ProviderDrop{name, "not a provider name"})
			continue
		}
		if len(out) >= component.MaxEgressProviders {
			dropped = append(dropped, ProviderDrop{name, fmt.Sprintf("more than %d providers", component.MaxEgressProviders)})
			continue
		}
		file := filepath.Join(dir, name, component.EgressProviderFileName)
		st, err := os.Lstat(file)
		if err != nil {
			continue // no descriptor: nothing announced
		}
		if !st.Mode().IsRegular() || st.Size() > maxProviderFileBytes {
			dropped = append(dropped, ProviderDrop{name, "the descriptor is not a plain file of at most 16 KiB"})
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			dropped = append(dropped, ProviderDrop{name, "the descriptor cannot be read"})
			continue
		}
		var f component.EgressProviderFile
		if json.Unmarshal(raw, &f) != nil || f.Contract != component.EgressProviderContract {
			dropped = append(dropped, ProviderDrop{name, "the descriptor is not " + component.EgressProviderContract})
			continue
		}
		u, err := loopbackURL(f.URL)
		if err != nil {
			dropped = append(dropped, ProviderDrop{name, err.Error()})
			continue
		}
		out = append(out, &LocalProvider{Name: name, url: u, token: f.Token})
	}
	return out, dropped
}

// CoreProvider is a core's own address as a local provider: a core that
// advertises core.CapabilityEgressProvider serves the provider paths at its
// endpoint, with the endpoint's bearer. Its address is held to the same
// loopback rule as a descriptor's.
func CoreProvider(name, baseURL, token string) (*LocalProvider, error) {
	if !localName.MatchString(name) {
		return nil, errors.New("not a provider name")
	}
	u, err := loopbackURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &LocalProvider{Name: name, url: u, token: token}, nil
}

// loopbackURL accepts an http address on this computer with a port, and
// nothing else: a provider is local, never a way to reach another host.
func loopbackURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("the address is not an http URL")
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		return nil, errors.New("the address has no port")
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("the address is not on this computer (loopback only)")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// providerClient talks to providers: no proxy, no redirects, a bounded wait.
var providerClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// call sends one request to the provider with the attested turn.
func (p *LocalProvider) call(ctx context.Context, method, path string, turn component.ProviderTurn, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, providerAsk)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, p.url.String()+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set(component.TurnHeader, component.EncodeHeader(turn))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := providerClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderAnswer+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxProviderAnswer {
		return nil, errors.New("the answer is larger than 1 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &StatusError{Status: resp.StatusCode, Body: raw}
	}
	return raw, nil
}

// Hosts asks which hosts the provider handles for the turn.
func (p *LocalProvider) Hosts(ctx context.Context, turn component.ProviderTurn) (component.EgressHosts, error) {
	var h component.EgressHosts
	raw, err := p.call(ctx, http.MethodGet, component.EgressProviderHostsPath, turn, nil)
	if err != nil {
		return h, err
	}
	err = json.Unmarshal(raw, &h)
	return h, err
}

// ask is the exchange's question for a turn: the request without its body.
func (p *LocalProvider) ask(turn component.ProviderTurn) asker {
	return func(ctx context.Context, in component.EgressRequest) ([]byte, error) {
		in.Body = nil
		return p.call(ctx, http.MethodPost, component.EgressProviderRequestPath, turn, in)
	}
}

// report tells the provider what a relayed request did. An answer that is
// lost costs a counter, never the request.
func (p *LocalProvider) report(turn component.ProviderTurn, u component.EgressUsage) {
	go func() {
		if _, err := p.call(context.Background(), http.MethodPost, component.EgressProviderReportPath, turn, u); err != nil {
			slog.Debug("bnbloc bridge egress report not delivered", "event", "bnbloc.bridge.egress_report_failed", "provider", p.Name, "error", err)
		}
	}()
}

// SetLocalProviders replaces the local providers the session asks (the
// bridge reads them at each turn's start).
func (s *Session) SetLocalProviders(ps []*LocalProvider) {
	m := map[string]*LocalProvider{}
	for _, p := range ps {
		m[p.Service()] = p
	}
	s.mu.Lock()
	s.local = m
	s.mu.Unlock()
}

// localProvider is the provider behind a service name of the session, if it
// is one. Only the member's own run is ever asked: a claim is set for a turn
// that is not (the bridge claims nothing then), and a turn that is not own
// here answers no question either (checked again at the call).
func (s *Session) localProvider(service string) (*LocalProvider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.local[service]
	return p, ok && s.job.OwnRun
}

// providerTurn is the turn a provider is told about: the facts as the
// bridge has them, and the agent. A provider only hears of own runs.
func (s *Session) providerTurn(job Job) component.ProviderTurn {
	return ProviderTurnOf(s.scope, job)
}

// ProviderTurnOf is the turn a provider is told about for scope and job.
func ProviderTurnOf(sc Scope, job Job) component.ProviderTurn {
	return component.ProviderTurn{
		Turn: component.Turn{Org: sc.TenantID, Room: sc.RoomID, Channel: job.ChannelID, Job: job.JobID,
			Requester: job.RequesterUserID, AgentOwner: sc.Owner, Drive: component.DriveOwn},
		Core: sc.Core, Provider: sc.Provider,
	}
}

// serviceLabel is how a message names the component it asked.
func serviceLabel(service string) string {
	if name, ok := strings.CutPrefix(service, LocalPrefix); ok {
		return "the " + name + " provider on this computer"
	}
	return "the " + service + " service"
}

// cardComponent is the component a card's actions open: a local provider's
// own name.
func cardComponent(service string) string { return strings.TrimPrefix(service, LocalPrefix) }
