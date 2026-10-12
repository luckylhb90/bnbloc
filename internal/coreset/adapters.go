package coreset

import (
	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// adapterDescriptor is the registry instance for an env-declared protocol
// adapter (a core driven via BNBLOC_CORE_<NAME>_URL that nothing installed).
// It is deliberately NOT a core.Provider, matching the sidecar-package
// descriptor: the entry FromEnv already built (with its protocol client) is
// what drives turns, while this registration exists only so /v1/cores — the
// availability truth the UI and desktop rely on — lists the core instead of
// dead-ending it. core.New keeps failing for the name, which is correct: the
// drive path stays on the explicit-URL branch.
type adapterDescriptor struct{}

// registerEnvAdapters registers a listing descriptor for every URL-declared
// entry that nothing else has registered (a built-in, manifest, or installed
// package covers those). Idempotent; never clobbers an existing registration.
func registerEnvAdapters(entries []Entry) {
	for _, e := range entries {
		if !e.Explicit {
			continue
		}
		if _, ok := core.Describe(e.Name); ok {
			continue
		}
		// extreg.Reregister directly (not core.Reregister) so Config stays
		// empty: a nil Factory would type-assert successfully in core.New and
		// panic when called.
		_ = extreg.Reregister(extreg.Extension{
			Kind:     core.KindCore,
			Name:     e.Name,
			Backend:  core.BackendSidecar,
			Instance: adapterDescriptor{},
		})
	}
}
