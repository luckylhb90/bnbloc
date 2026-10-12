package manifestcore

import (
	"fmt"
	"sort"

	"github.com/luckylhb90/bnbloc/core"
)

// LoadManifests registers each raw manifest (JSON bytes) as a runtime core
// provider under its manifest Name via core.Reregister, so an installed manifest
// becomes a selectable core with no recompile and no filesystem. The caller
// (which owns persistence — the gateway reads them from platform_settings)
// supplies the bytes; manifestcore stays store-agnostic. Best-effort: a
// malformed manifest is skipped and reported in errs, so one bad entry never
// blocks the rest. Returns the registered names (sorted) and per-manifest errors.
//
// Reregister (not Register) is used deliberately: it is idempotent and refuses
// to overwrite an already-registered core, so both boot-time rehydration and
// runtime re-install can re-run this safely without panicking on a duplicate.
func LoadManifests(raws [][]byte) (registered []string, errs []error) {
	for i, raw := range raws {
		prov, parseErr := Parse(raw)
		if parseErr != nil {
			errs = append(errs, indexErr(i, parseErr))
			continue
		}
		name := prov.Name()
		// Fresh Provider per construction so the registry factory does not alias
		// one instance across callers; optional capabilities are reported by
		// Provider.Supports from the manifest's declared ops.
		m := prov.m
		desc := core.CoreExtension{Backend: core.BackendManifest, Instance: &m}
		if err := core.Reregister(name, func() core.Provider { p, _ := New(m); return p }, desc); err != nil {
			errs = append(errs, indexErr(i, err))
			continue
		}
		registered = append(registered, name)
	}
	sort.Strings(registered)
	return registered, errs
}

type loadError struct {
	index int
	err   error
}

func (e loadError) Error() string {
	return fmt.Sprintf("manifestcore: manifest %d: %v", e.index, e.err)
}

func indexErr(i int, err error) error { return loadError{index: i, err: err} }
