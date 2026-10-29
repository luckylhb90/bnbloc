package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// Service components for a turn (docs/architecture/service-components.md):
// the organization's services that declare turnProxy, asked through the
// gateway's turn proxy for the job being driven. roomFacts adds a line to
// the session guidance's list of records and files under .room/<service>/;
// egress claims hosts the session's commands reach through the service
// (the room tools server's proxy, roomtools/egress.go).

// turnServicesTTL is how long the list of services is kept.
const turnServicesTTL = time.Minute

// factsTTL is how long a job's facts are kept: the session's first turn
// asks for them twice (the guidance, the records).
const factsTTL = 30 * time.Second

type turnServices struct {
	mu    sync.Mutex
	at    time.Time
	list  []wire.TurnService
	facts map[string]jobFacts
}

type jobFacts struct {
	at    time.Time
	facts []serviceFacts
}

// serviceFacts is one service's facts for a job (err: unreadable).
type serviceFacts struct {
	name  string
	facts component.Facts
	err   error
}

// turnServiceList is the organization's services a turn may call (cached;
// an unreadable list is no services, logged).
func (b *Bridge) turnServiceList(ctx context.Context) []wire.TurnService {
	c := &b.services
	c.mu.Lock()
	if !c.at.IsZero() && time.Since(c.at) < turnServicesTTL {
		list := c.list
		c.mu.Unlock()
		return list
	}
	c.mu.Unlock()
	var out wire.TurnServiceList
	raw, err := b.gatewayGET(ctx, "/v1/my/turn-services")
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	if err != nil {
		slog.Debug("bnbloc bridge turn services unreadable", "event", "bnbloc.bridge.turn_services_failed", "error", err)
	}
	c.mu.Lock()
	c.at, c.list = time.Now(), out.Services
	c.mu.Unlock()
	return out.Services
}

// servicesWith names the services declaring capability, sorted.
func (b *Bridge) servicesWith(ctx context.Context, capability string) []string {
	var out []string
	for _, s := range b.turnServiceList(ctx) {
		if slices.Contains(s.Capabilities, capability) {
			out = append(out, s.Name)
		}
	}
	sort.Strings(out)
	return out
}

// turnProxyGET asks one service for a job through the gateway's turn proxy.
func (b *Bridge) turnProxyGET(ctx context.Context, service, path, room, job string, out any) error {
	raw, err := b.gatewayGET(ctx, component.TurnProxyPath+url.PathEscape(service)+"/"+path+"?room="+url.QueryEscape(room)+"&job="+url.QueryEscape(job))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// jobFacts are the facts of every roomFacts service for the job (asked
// once per factsTTL).
func (b *Bridge) jobFacts(ctx context.Context, room, job string) []serviceFacts {
	names := b.servicesWith(ctx, component.ServiceRoomFacts)
	if len(names) == 0 || job == "" {
		return nil
	}
	c := &b.services
	c.mu.Lock()
	if f, ok := c.facts[job]; ok && time.Since(f.at) < factsTTL {
		c.mu.Unlock()
		return f.facts
	}
	c.mu.Unlock()
	out := make([]serviceFacts, 0, len(names))
	for _, name := range names {
		if _, ok := roomlog.ServiceDir(name); !ok {
			slog.Warn("bnbloc bridge service facts skipped: its name is one of the room records'", "event", "bnbloc.bridge.service_facts_reserved", "service", name)
			continue
		}
		var f component.Facts
		err := b.turnProxyGET(ctx, name, "facts", room, job, &f)
		if err == nil {
			err = component.CheckFacts(f)
		}
		if err != nil {
			slog.Warn("bnbloc bridge service facts unreadable", "event", "bnbloc.bridge.service_facts_failed", "service", name, "room", room, "job", job, "error", err)
			f = component.Facts{}
		}
		out = append(out, serviceFacts{name: name, facts: f, err: err})
	}
	c.mu.Lock()
	if c.facts == nil {
		c.facts = map[string]jobFacts{}
	}
	now := time.Now()
	for k, v := range c.facts {
		if now.Sub(v.at) >= factsTTL {
			delete(c.facts, k)
		}
	}
	c.facts[job] = jobFacts{at: now, facts: out}
	c.mu.Unlock()
	return out
}

// serviceFactLines are the services' lines for the session guidance.
func serviceFactLines(facts []serviceFacts) []roomlog.ServiceLine {
	var out []roomlog.ServiceLine
	for _, f := range facts {
		if f.err == nil && f.facts.Line == "" {
			continue
		}
		out = append(out, roomlog.ServiceLine{Service: f.name, Line: f.facts.Line, Unreadable: f.err != nil})
	}
	return out
}

// serviceFactFiles are the services' files under .room/<service>/.
func serviceFactFiles(facts []serviceFacts) []roomlog.NamedFile {
	var out []roomlog.NamedFile
	for _, f := range facts {
		dir, ok := roomlog.ServiceDir(f.name)
		if !ok || f.err != nil {
			continue
		}
		names := make([]string, 0, len(f.facts.Files))
		for n := range f.facts.Files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, roomlog.NamedFile{Name: dir + n, Content: f.facts.Files[n]})
		}
	}
	return out
}

// egressFor asks every egress service which hosts it handles for the job
// and the environment it gives the session's commands: host → service
// (a host two services claim stays with the first by name, logged), and
// the merged environment, and the services that take a streamed body. A
// service that cannot be read keeps the hosts it claimed before (prev), so
// its hosts never go out unauthenticated (and takes no streamed body).
func (b *Bridge) egressFor(ctx context.Context, room, job string, prev map[string]string, own *component.ProviderTurn) egressClaims {
	hosts, env, streams, paths := map[string]string{}, map[string]string{}, map[string]bool{}, map[string][]string{}
	files := map[string]map[string]string{}
	var local []*roomtools.LocalProvider
	type source struct {
		name string
		ask  func() (component.EgressHosts, error)
	}
	var sources []source
	for _, name := range b.servicesWith(ctx, component.ServiceEgress) {
		sources = append(sources, source{name, func() (h component.EgressHosts, err error) {
			err = b.turnProxyGET(ctx, name, "egress/hosts", room, job, &h)
			return
		}})
	}
	// Providers on this computer are asked about the member's own run only:
	// any other run claims nothing from them and never reaches them.
	if own != nil {
		for _, p := range b.localProviders(ctx) {
			local = append(local, p)
			sources = append(sources, source{p.Service(), func() (component.EgressHosts, error) { return p.Hosts(ctx, *own) }})
		}
	}
	for _, src := range sources {
		name := src.name
		h, err := src.ask()
		if err != nil {
			slog.Warn("bnbloc bridge egress hosts unreadable", "event", "bnbloc.bridge.egress_hosts_failed", "service", name, "room", room, "job", job, "error", err)
			for host, svc := range prev {
				if svc == name {
					hosts[host] = name
				}
			}
			continue
		}
		if h.Streams {
			streams[name] = true
		}
		for _, host := range h.Hosts {
			if len(hosts) >= component.MaxEgressHosts*4 || !component.ValidEgressHost(host) {
				continue
			}
			if other, taken := hosts[host]; taken && other != name {
				slog.Warn("bnbloc bridge egress host claimed twice", "event", "bnbloc.bridge.egress_host_conflict", "host", host, "kept", other, "service", name)
				continue
			}
			hosts[host] = name
			if ps := validPaths(h.Paths[host]); len(ps) > 0 {
				paths[host] = ps
			}
		}
		if fs := validProviderFiles(h.Files); len(fs) > 0 && strings.HasPrefix(name, roomtools.LocalPrefix) {
			files[strings.TrimPrefix(name, roomtools.LocalPrefix)] = fs
		}
		for k, v := range h.Env {
			if _, taken := env[k]; !taken && validEnvName(k) {
				env[k] = strings.ReplaceAll(v, "{files}", providerFilesToken(name))
			}
		}
	}
	slog.Info("bnbloc bridge egress hosts", "event", "bnbloc.bridge.egress_hosts", "room", room, "job", job, "hosts", len(hosts))
	return egressClaims{hosts: hosts, env: env, streams: streams, paths: paths, local: local, files: files}
}

// providerFilesToken stands for a provider's files folder in an Env value
// until the folder's path is known (the workspace's, writeProviderFiles).
func providerFilesToken(service string) string {
	return "{files:" + strings.TrimPrefix(service, roomtools.LocalPrefix) + "}"
}

// validProviderFiles keeps the files a provider may hand a run: valid names, at
// most component.MaxProviderFiles and component.MaxProviderFilesBytes together.
func validProviderFiles(in map[string]string) map[string]string {
	names := make([]string, 0, len(in))
	for n := range in {
		names = append(names, n)
	}
	sort.Strings(names)
	out := map[string]string{}
	total := 0
	for _, n := range names {
		if !component.ValidProviderFile(n) || len(out) >= component.MaxProviderFiles || total+len(in[n]) > component.MaxProviderFilesBytes {
			continue
		}
		out[n] = in[n]
		total += len(in[n])
	}
	return out
}

// hasEgress reports whether a session may have the egress proxy: an
// organization declares an egress service, or (a sandboxed session only) a
// provider on this computer exists. A host session of the member's own never
// goes through a local provider: its agent holds its own credentials.
func (b *Bridge) hasEgress(ctx context.Context, sandboxed bool) bool {
	return len(b.servicesWith(ctx, component.ServiceEgress)) > 0 || (sandboxed && len(b.localProviders(ctx)) > 0)
}

// ownTurn is the turn a local provider is told about, or nil unless the job
// is the member's own run in the sandbox (providers are never asked for any
// other run).
func ownTurn(scope roomtools.Scope, job roomtools.Job, sandboxed bool) *component.ProviderTurn {
	if !job.OwnRun || !sandboxed {
		return nil
	}
	t := roomtools.ProviderTurnOf(scope, job)
	return &t
}

// localProviders are the providers on this computer: what a program
// announced under the state directory (component.EgressProviderContract),
// and every core that advertises core.CapabilityEgressProvider (its own
// address, a name a descriptor of the same name does not replace).
func (b *Bridge) localProviders(ctx context.Context) []*roomtools.LocalProvider {
	dir := b.cfg.EgressProvidersDir
	if dir == "" && b.cfg.StateDir != "" {
		dir = filepath.Join(b.cfg.StateDir, "egress-providers")
	}
	ps, dropped := roomtools.ReadLocalProviders(dir)
	for _, d := range dropped {
		slog.Warn("bnbloc bridge egress provider dropped", "event", "bnbloc.bridge.egress_provider_dropped", "provider", d.String())
	}
	taken := map[string]bool{}
	for _, p := range ps {
		taken[p.Name] = true
	}
	b.mu.Lock()
	names := make([]string, 0, len(b.cores))
	for name := range b.cores {
		names = append(names, name)
	}
	b.mu.Unlock()
	sort.Strings(names)
	for _, name := range names {
		e, ok := b.coreEntry(name)
		if !ok || taken[name] || len(ps) >= component.MaxEgressProviders || !corepkg.Supports(ctx, e.provider, e.endpoint, corepkg.CapabilityEgressProvider) {
			continue
		}
		p, err := roomtools.CoreProvider(name, e.endpoint.BaseURL, e.endpoint.Token)
		if err != nil {
			slog.Warn("bnbloc bridge egress provider dropped", "event", "bnbloc.bridge.egress_provider_dropped", "provider", name+": "+err.Error())
			continue
		}
		ps = append(ps, p)
	}
	return ps
}

// egressClaims are what the egress services claim for a job: host →
// service, the merged environment, the services that take a streamed
// body, and the hosts handled only on some paths (component.EgressHosts.Paths).
type egressClaims struct {
	hosts   map[string]string
	env     map[string]string
	streams map[string]bool
	paths   map[string][]string
	// local are the providers on this computer asked for the job (the
	// session answers their hosts' requests through them).
	local []*roomtools.LocalProvider
	// files are the local providers' files, by provider name: written under
	// the sandboxed workspace's .room/egress/<provider>/ at each turn.
	files map[string]map[string]string
}

// validPaths keeps a claim's path prefixes that start with "/" (at most 64).
func validPaths(in []string) []string {
	var out []string
	for _, p := range in {
		if strings.HasPrefix(p, "/") && len(p) <= 256 && !strings.ContainsAny(p, "?#") && len(out) < 64 {
			out = append(out, p)
		}
	}
	return out
}

// validEnvName accepts a POSIX environment name that is not bnbloc's own
// or the proxy's.
func validEnvName(k string) bool {
	if k == "" || len(k) > 64 || slices.Contains([]string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "PATH", "HOME"}, k) {
		return false
	}
	for i, r := range k {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return len(k) < 7 || k[:7] != "BNBLOC_"
}
