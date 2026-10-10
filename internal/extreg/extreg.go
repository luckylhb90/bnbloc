// Package extreg is the kind-agnostic extension registry: the single in-memory
// truth for every installed/built-in extension across all kinds (core, auth,
// webhook, …). It generalizes the core-only registry (Backend⟂Source model)
// into a registry keyed by (kind, name), driven by a KindDescriptor seam so a
// new kind plugs in without touching the shared install/persist/list pipeline.
//
// extreg imports NO kind package: Config and Instance are held as `any` so the
// registry never depends on a kind's concrete types (a kind package imports
// extreg, never the reverse). Callers that need the concrete value type-assert.
package extreg

import (
	"fmt"
	"sort"
	"sync"
)

// Service names a process that can host extension kinds, so a kind declares
// which services activate it (a core runs in gateway+bridge; an auth source
// only in the gateway; a webhook only in realtime).
type Service string

const (
	ServiceGateway  Service = "gateway"
	ServiceBridge   Service = "bridge"
	ServiceRealtime Service = "realtime"
)

// Kind is an extension category with its own typed contract (core, auth, …).
type Kind string

// Backend is HOW an extension is driven: a compiled Go implementation, the
// manifest interpreter over templated REST, or an out-of-process sidecar.
type Backend string

const (
	BackendNative   Backend = "native"
	BackendManifest Backend = "manifest"
	BackendSidecar  Backend = "sidecar"
	// BackendBundle is a static web bundle served to a sandboxed frame
	// (previewers): nothing is driven or supervised.
	BackendBundle Backend = "bundle"
)

// Source is WHERE an extension came from: compiled into the binary (a
// pre-installed, non-removable extension) or installed at runtime via the API.
// Orthogonal to Backend — a shipped manifest is {manifest,builtin}, a third-party manifest
// is {manifest,installed}.
type Source string

const (
	SourceBuiltin   Source = "builtin"
	SourceInstalled Source = "installed"
)

// Extension is the unified registry record for one extension of any kind.
// Removable gates uninstall; Backend/Source classify it for the API. Config is
// the decoded per-kind typed config (e.g. a *manifestcore.Manifest or a Factory
// for a native core); Instance is the live driver once activated. Both are
// `any` so the registry stays independent of every kind's package.
type Extension struct {
	Kind      Kind
	Name      string
	Backend   Backend
	Source    Source
	Removable bool
	Version   string
	Config    any
	Instance  any
}

func key(kind Kind, name string) string { return string(kind) + "\x00" + name }

// KindDescriptor is the seam a kind implements to plug into the shared pipeline.
// The pipeline calls these to validate, activate, and tear down an extension of
// this kind without knowing the kind's concrete contract. Hosts declares which
// services activate the kind.
type KindDescriptor struct {
	Kind          Kind
	Contract      KindContract
	Hosts         []Service
	HookEnvelopes bool
}

var (
	mu    sync.RWMutex
	exts  = map[string]Extension{}
	kinds = map[Kind]KindDescriptor{}
)

// RegisterKind registers a kind's descriptor. It panics on a duplicate so a
// wiring mistake fails loudly at startup, matching the driver-registration idiom.
func RegisterKind(d KindDescriptor) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := kinds[d.Kind]; dup {
		panic("extreg: duplicate kind registration: " + string(d.Kind))
	}
	d.Hosts = append([]Service(nil), d.Hosts...)
	kinds[d.Kind] = d
}

// Descriptor returns a registered kind's descriptor.
func Descriptor(k Kind) (KindDescriptor, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := kinds[k]
	d.Hosts = append([]Service(nil), d.Hosts...)
	return d, ok
}

// Kinds lists registered kind names (sorted).
func Kinds() []Kind {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Kind, 0, len(kinds))
	for k := range kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// RegisterBuiltin records a compiled-in, non-removable extension. It panics on a
// duplicate (kind,name) so blank-import wiring mistakes fail at startup.
func RegisterBuiltin(ext Extension) {
	ext.Source = SourceBuiltin
	ext.Removable = false
	mu.Lock()
	defer mu.Unlock()
	if _, dup := exts[key(ext.Kind, ext.Name)]; dup {
		panic(fmt.Sprintf("extreg: duplicate registration: %s/%s", ext.Kind, ext.Name))
	}
	exts[key(ext.Kind, ext.Name)] = ext
}

// Reregister installs or replaces an extension at runtime without panicking on a
// duplicate. It refuses to overwrite a non-removable built-in, so a runtime
// install can never shadow a built-in. Returns an error (not a panic) so a bad
// install is a handled result.
func Reregister(ext Extension) error {
	mu.Lock()
	defer mu.Unlock()
	if existing, ok := exts[key(ext.Kind, ext.Name)]; ok && !existing.Removable {
		return fmt.Errorf("extreg: %s/%s is a built-in and cannot be replaced", ext.Kind, ext.Name)
	}
	exts[key(ext.Kind, ext.Name)] = ext
	return nil
}

// Unregister removes a runtime-installed (removable) extension. Removing a
// non-removable built-in is refused; a missing entry is a no-op.
func Unregister(kind Kind, name string) error {
	mu.Lock()
	defer mu.Unlock()
	if existing, ok := exts[key(kind, name)]; ok && !existing.Removable {
		return fmt.Errorf("extreg: %s/%s is a built-in and cannot be removed", kind, name)
	}
	delete(exts, key(kind, name))
	return nil
}

// Get returns the extension registered under (kind, name).
func Get(kind Kind, name string) (Extension, bool) {
	mu.RLock()
	defer mu.RUnlock()
	e, ok := exts[key(kind, name)]
	return e, ok
}

// SetInstance stores the live driver for an already-registered extension (set
// by the pipeline at activation). No-op if the entry is gone.
func SetInstance(kind Kind, name string, instance any) {
	mu.Lock()
	defer mu.Unlock()
	if e, ok := exts[key(kind, name)]; ok {
		e.Instance = instance
		exts[key(kind, name)] = e
	}
}

// List returns extensions of one kind (sorted by name); an empty kind lists all
// (sorted by kind then name).
func List(kind Kind) []Extension {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Extension, 0, len(exts))
	for _, e := range exts {
		if kind == "" || e.Kind == kind {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}
