// Package coreset parses the multi-core configuration shared by the gateway and
// bridge composition roots into an ordered list of ready-to-use cores. It turns
// the BNBLOC_CORES env list into constructed core.Provider instances, wires
// each core's discovery inputs from the environment, and fills each core's
// Endpoint from an explicit per-core override or best-effort discovery. Keeping
// this in one place means
// the gateway and bridge agree byte-for-byte on how cores are selected and
// addressed.
package coreset

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/config"
)

// Entry is one configured core: its name, the constructed provider, and the
// endpoint (explicit override or discovered) the caller uses to reach it. A
// zero Endpoint means the daemon was not found yet; callers keep the entry and
// resolve it later through Rediscover (its targets come back empty until the
// daemon is up) — dropping it would lose a core that starts after bnbloc.
type Entry struct {
	Name     string
	Provider core.Provider
	Endpoint core.Endpoint
	// Explicit is true when Endpoint came from a configured BNBLOC_CORE_<NAME>_URL
	// override rather than discovery. An explicit endpoint is fixed and must
	// never be overwritten by (re)discovery; a discovered one may be refreshed
	// so the caller follows a daemon that restarted onto a new address.
	Explicit bool
}

// discoverTimeout bounds the best-effort startup discovery of each core so a
// slow or absent daemon cannot stall composition-root startup.
const discoverTimeout = 3 * time.Second

// FromEnv builds the ordered core list of BNBLOC_CORES (comma-separated core
// names); when unset it yields no env-configured cores (no baked-in default).
// Each registered name becomes EntryFor(name, provider); an unknown name with
// no URL is skipped with a warning, and an unknown name WITH a URL becomes a
// lazily discovering Core Protocol client. Order follows the configured list
// so the first entry is the default core. Installed cores are not listed
// here: the gateway activates them from its durable records (through the
// same EntryFor), and desktop-managed sidecar cores hand the bridge a
// transient manifest directory through BNBLOC_CORE_MANIFESTS_DIR.
func FromEnv() []Entry {
	loadEnvManifests()
	names := splitCSV(config.FromEnvRaw("BNBLOC_CORES"))
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		p, err := core.New(name)
		if err != nil {
			if url, _ := envEndpoint(name); url != "" {
				// A URL-declared Core Protocol adapter nothing registered. The
				// client discovers lazily (and re-discovers), so a daemon that is
				// not up yet at boot is still configured and is used as soon as
				// it answers — dropping it here would lose it until a restart.
				entries = append(entries, EntryFor(name, protocol.NewClient(name, nil), core.Endpoint{}))
				continue
			}
			// A configured core that is not registered (a typo, or a not-yet-loaded
			// manifest/extension) must not crash the process: skip it with a loud
			// warning and continue with the cores that ARE available, so a bad
			// entry in user-editable settings cannot crash-loop the bridge/gateway.
			log.Printf("coreset: skipping unknown core %q (known: %v)", name, core.Names())
			continue
		}
		entries = append(entries, EntryFor(name, p, core.Endpoint{}))
	}
	registerEnvAdapters(entries)
	return entries
}

// EntryFor is the one way a core's Entry is built, for env-configured and
// installed cores alike, so BNBLOC_CORE_<NAME>_URL/_TOKEN means the same in
// the gateway and the bridge. An explicit URL override wins (Explicit). Else
// declared, the address an endpoint-runtime package names, is used as fixed
// configuration (the token override still applies). Else a core that can
// self-discover is discovered best-effort within discoverTimeout; an entry
// whose daemon is not up yet keeps a zero endpoint for Rediscover. A manifest
// core's probe inputs (its token, a repointed URL) are wired from the same
// overrides: they live at the composition root, so the core packages keep no
// OS or container assumptions.
func EntryFor(name string, p core.Provider, declared core.Endpoint) Entry {
	url, token := envEndpoint(name)
	if mp, ok := p.(*manifestcore.Provider); ok {
		mp.ProbeToken = token
		mp.ProbeURLOverride = url
	}
	switch {
	case url != "":
		return Entry{Name: name, Provider: p, Endpoint: core.Endpoint{BaseURL: url, Token: token}, Explicit: true}
	case strings.TrimSpace(declared.BaseURL) != "":
		if token != "" {
			declared.Token = token
		}
		return Entry{Name: name, Provider: p, Endpoint: declared, Explicit: true}
	}
	e := Entry{Name: name, Provider: p}
	if d, ok := p.(core.Discoverer); ok {
		ctx, cancel := context.WithTimeout(context.Background(), discoverTimeout)
		if found, ok := d.Discover(ctx); ok {
			e.Endpoint = found
		}
		cancel()
	}
	return e
}

// envEndpoint reads a core's BNBLOC_CORE_<NAME>_URL / _TOKEN overrides.
func envEndpoint(name string) (url, token string) {
	upper := EnvKey(name)
	return strings.TrimSpace(config.FromEnvRaw("BNBLOC_CORE_" + upper + "_URL")),
		strings.TrimSpace(config.FromEnvRaw("BNBLOC_CORE_" + upper + "_TOKEN"))
}

func loadEnvManifests() {
	dir := strings.TrimSpace(config.FromEnvRaw("BNBLOC_CORE_MANIFESTS_DIR"))
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("coreset: load manifests dir %q: %v", dir, err)
		return
	}
	raws := make([][]byte, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			log.Printf("coreset: read manifest %q: %v", entry.Name(), err)
			continue
		}
		raws = append(raws, raw)
	}
	_, errs := manifestcore.LoadManifests(raws)
	for _, err := range errs {
		log.Printf("coreset: %v", err)
	}
}

// Rediscover keeps a core's endpoint current. It never touches an explicit
// endpoint (a configured BNBLOC_CORE_<NAME>_URL override) or a core that cannot
// self-discover. For a discovery-capable core it re-runs the bounded discovery
// every call and adopts the result: this both fills an endpoint that was empty
// at boot (a daemon started later) AND follows a daemon that RESTARTED onto a
// new address — a local daemon that binds a random port and mints a
// random token per run, so a cached boot-time endpoint would go stale on every
// restart and every subsequent call would hit a dead port. When the daemon is
// momentarily absent (discovery fails) the last-known endpoint is kept, so a
// transient miss does not blank out a still-reachable core. The provider's
// discovery inputs (listener path, probe URL/token) were wired in FromEnv and
// persist on the provider, so no re-wiring is needed here.
func Rediscover(ctx context.Context, e *Entry) core.Endpoint {
	if e == nil {
		return core.Endpoint{}
	}
	if e.Explicit {
		return e.Endpoint // fixed configured address: never rediscover
	}
	d, ok := e.Provider.(core.Discoverer)
	if !ok {
		return e.Endpoint
	}
	dctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	if found, ok := d.Discover(dctx); ok && found.OK() {
		e.Endpoint = found // adopt: fills-if-empty and follows a restart
	}
	return e.Endpoint
}

// splitCSV parses a comma-separated env value into a trimmed, non-empty list.
func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// EnvKey is the one rule naming a core's env overrides: the core name
// upper-cased with every character outside [A-Za-z0-9] replaced by '_', as in
// BNBLOC_CORE_<EnvKey(name)>_URL. The desktop shell writes the same keys
// (env_key in apps/desktop); testdata/core_env_keys.json pins both.
func EnvKey(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, strings.ToUpper(name))
}
