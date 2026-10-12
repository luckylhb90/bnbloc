package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
)

func (s *Service) memberInstalledComponents(ctx context.Context, subject string) (map[string]int64, error) {
	out := map[string]int64{}
	if s.ComponentInstalls == nil || subject == "" {
		return out, nil
	}
	installs, err := s.ComponentInstalls.ListComponentInstalls(ctx, subject)
	if err != nil {
		return nil, err
	}
	for _, in := range installs {
		out[componentKey(in.Kind, in.Name)] = in.InstalledAtMs
	}
	return out, nil
}

func componentKey(kind, name string) string { return kind + "/" + name }

func componentKindForName(rec extpipe.Record) extreg.Kind { return extreg.Kind(rec.Kind) }

// errAmbiguousComponent reports a component name shared by several kinds (a
// core and a plugin may both be called "auditor") with no kind to pick one.
var errAmbiguousComponent = errors.New("component name is ambiguous across kinds; pass ?kind=")

// findComponentRecord looks an installed component up by kind and name. A
// core and a plugin may share a name, so with an empty kind the lookup only
// succeeds when exactly one kind has that name (errAmbiguousComponent
// otherwise) — it never silently picks one.
func (s *Service) findComponentRecord(ctx context.Context, kind extreg.Kind, name string) (extpipe.Record, bool, error) {
	recs, err := s.corePipeline().List(ctx, kind)
	if err != nil {
		return extpipe.Record{}, false, err
	}
	var found []extpipe.Record
	for _, rec := range recs {
		if rec.Name == name {
			found = append(found, rec)
		}
	}
	switch len(found) {
	case 0:
		return extpipe.Record{}, false, nil
	case 1:
		return found[0], true, nil
	default:
		return extpipe.Record{}, false, errAmbiguousComponent
	}
}

// requestComponentRecord resolves the {name} component of a request, using
// the optional ?kind= query parameter to disambiguate. It writes the error
// response (404 / 409 / 500) and reports ok=false when there is no single
// match.
func (s *Service) requestComponentRecord(w http.ResponseWriter, r *http.Request, name string) (extpipe.Record, bool) {
	kind := extreg.Kind(strings.TrimSpace(r.URL.Query().Get("kind")))
	rec, ok, err := s.findComponentRecord(r.Context(), kind, name)
	switch {
	case errors.Is(err, errAmbiguousComponent):
		httpx.Error(w, err.Error(), http.StatusConflict)
		return extpipe.Record{}, false
	case err != nil:
		obs.ServerError(w, "load components failed", err)
		httpx.Error(w, "load components failed", http.StatusInternalServerError)
		return extpipe.Record{}, false
	case !ok:
		httpx.Error(w, "component not found", http.StatusNotFound)
		return extpipe.Record{}, false
	}
	return rec, true
}

// componentKindVisibleInMemberCatalog names the kinds a member's desktop
// consumes from GET /v1/components (and may download artifacts of): cores and
// plugins it supervises, and sandbox runtimes it will supervise / the bridge
// will drive, previewer bundles members' clients render files with, and MCP
// tools (stdio binaries the desktop materializes, http endpoints the agent
// calls). Endpoint-only kinds driven server-side (auth providers, notifiers)
// never reach member machines.
func componentKindVisibleInMemberCatalog(kind extreg.Kind) bool {
	return kind == core.KindCore || kind == pkgmeta.KindPlugin || kind == extreg.KindSandbox || kind == extreg.KindPreviewer || kind == extreg.KindMCP
}

// memberComponents is one request's read of the install records and the
// caller's opt-in installs. Every member-surface gate (/v1/cores, targets,
// run, artifacts) decides from it, so a listing costs two store reads instead
// of a records scan per core.
type memberComponents struct {
	viewer    extpipe.Viewer
	records   map[string]extpipe.Record // componentKey(kind, name) -> record
	installed map[string]int64          // componentKey(kind, name) -> opt-in install time
}

func (s *Service) loadMemberComponents(ctx context.Context, claims auth.Claims) (memberComponents, error) {
	recs, err := s.corePipeline().List(ctx, "")
	if err != nil {
		return memberComponents{}, err
	}
	installed, err := s.memberInstalledComponents(ctx, claims.Subject)
	if err != nil {
		return memberComponents{}, err
	}
	view := memberComponents{viewer: viewerOf(claims), records: make(map[string]extpipe.Record, len(recs)), installed: installed}
	for _, rec := range recs {
		view.records[componentKey(rec.Kind, rec.Name)] = rec
	}
	return view, nil
}

func (m memberComponents) record(kind extreg.Kind, name string) (extpipe.Record, bool) {
	rec, ok := m.records[componentKey(string(kind), name)]
	return rec, ok
}

func (m memberComponents) visibility(rec extpipe.Record) extpipe.Visibility {
	return extpipe.MemberVisibility(m.viewer, extpipe.GateForRecord(rec), m.installed[componentKey(rec.Kind, rec.Name)])
}

// coreVisible reports whether a registered/configured core is usable by the
// member: an installed core passes the rollout + tenant gate; a core with no
// install record is visible when the operator configured it (BNBLOC_CORES /
// _URL). No core is compiled in, so there is no third kind.
func (m memberComponents) coreVisible(s *Service, name string) bool {
	rec, ok := m.record(core.KindCore, name)
	if !ok {
		return s.configuredCore(name)
	}
	return m.visibility(rec).Installed
}

func (s *Service) configuredCore(name string) bool {
	s.coresMu.RLock()
	defer s.coresMu.RUnlock()
	for _, entry := range s.Cores {
		if entry.Name == name {
			return true
		}
	}
	return false
}

// visibleCoreEntries snapshots the gateway's core entries the member may use.
func (s *Service) visibleCoreEntries(ctx context.Context, claims auth.Claims) ([]coreset.Entry, error) {
	view, err := s.loadMemberComponents(ctx, claims)
	if err != nil {
		return nil, err
	}
	s.coresMu.RLock()
	entries := append([]coreset.Entry(nil), s.Cores...)
	s.coresMu.RUnlock()
	out := make([]coreset.Entry, 0, len(entries))
	for _, entry := range entries {
		if view.coreVisible(s, entry.Name) {
			out = append(out, entry)
		}
	}
	return out, nil
}

// rediscoverCores refreshes the endpoints of the given core entries WITHOUT
// holding coresMu across network discovery: discovery runs on the caller's
// copies, and each refreshed endpoint is written back only if the shared entry
// still holds the endpoint it was discovered from (a concurrent install,
// uninstall, or refresh wins). It returns the refreshed copies.
func (s *Service) rediscoverCores(ctx context.Context, entries []coreset.Entry) []coreset.Entry {
	out := append([]coreset.Entry(nil), entries...)
	for i := range out {
		before := out[i].Endpoint
		if after := coreset.Rediscover(ctx, &out[i]); after != before {
			s.storeDiscoveredEndpoint(out[i].Name, before, after)
		}
	}
	return out
}

func (s *Service) storeDiscoveredEndpoint(name string, before, after core.Endpoint) {
	s.coresMu.Lock()
	defer s.coresMu.Unlock()
	for i := range s.Cores {
		if s.Cores[i].Name == name && s.Cores[i].Endpoint == before {
			s.Cores[i].Endpoint = after
			return
		}
	}
}

// viewerOf is the member a session acts as, for the one visibility gate
// (extpipe.MemberVisibility) every member surface decides through: off →
// hidden; optin → listed, usable only once the member installed it; auto →
// usable by everyone in an allowed tenant; a member's own content → theirs.
func viewerOf(claims auth.Claims) extpipe.Viewer {
	return extpipe.Viewer{Subject: claims.Subject, Tenant: claims.Tenant}
}
