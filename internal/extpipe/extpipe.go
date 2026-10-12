// Package extpipe is the kind-dispatched extension lifecycle pipeline: the one
// install / enable / disable / uninstall / list / rehydrate path shared by every
// extension kind. It generalizes the core-only install machinery so a new kind
// plugs in by registering a KindInstaller — the pipeline owns verification,
// persistence, governance, audit, and registry bookkeeping; the installer owns
// only the kind-specific decode/probe/activate/deactivate.
//
// The per-kind lifecycle vtable lives here (not in extreg) so extreg stays
// independent of pkgmeta/extgov/store; extpipe may import them freely.
package extpipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Record is the durable per-extension install record persisted in
// platform_settings under key ext.<kind>.<name>. Config is the kind-specific
// payload (the shared PackageConfig projection, see config.go).
//
// Distribution is governed by two read-time filters, enforced wherever the kind
// is listed or dispatched (never by deactivating the driver):
//   - Rollout: auto (everyone; the default, including the pre-rollout "" value),
//     optin (only members who installed it), off (registered but distributed to
//     nobody and not activated).
//   - EnabledTenants: nil means every tenant; a non-nil list restricts the
//     extension to exactly those tenants. The pipeline never persists an empty
//     non-nil list: SetTenants rejects it (fail closed) and ClearTenants is the
//     only way back to "all tenants". See AllowsTenant.
type Record struct {
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	Backend        string          `json:"backend"`
	Version        string          `json:"version"`
	Publisher      string          `json:"publisher,omitempty"`
	KeyID          string          `json:"keyId,omitempty"`
	ContentHash    string          `json:"contentHash,omitempty"`
	InstalledAtMs  int64           `json:"installedAtMs"`
	Config         json.RawMessage `json:"config,omitempty"`
	Network        []string        `json:"network,omitempty"`
	EnabledTenants []string        `json:"enabledTenants,omitempty"`
	Rollout        string          `json:"rollout,omitempty"`
}

// AllowsTenant reports whether the record is distributed to tenantID: every
// tenant when EnabledTenants is nil/empty (the only empty state the pipeline
// persists is nil — "all tenants"), else only the listed tenants.
func (r Record) AllowsTenant(tenantID string) bool {
	if len(r.EnabledTenants) == 0 {
		return true
	}
	for _, t := range r.EnabledTenants {
		if t == tenantID {
			return true
		}
	}
	return false
}

// active reports whether a record's driver should be live in its host service:
// every rollout except off. Opt-in records are active and gated per member at
// read time, exactly like auto records are gated per tenant.
func (r Record) active() bool { return NormalizeRollout(r.Rollout) != RolloutOff }

// KindInstaller is the per-kind lifecycle vtable. The pipeline calls it to turn
// a verified package into a persisted, activated extension without knowing the
// kind's concrete contract. All funcs are required except Deactivate.
type KindInstaller struct {
	Kind extreg.Kind
	// PackageKinds maps concrete package schema versions onto this extension kind.
	// Omit it when package.kind already equals Kind.
	PackageKinds []string

	// Decode extracts the kind's typed config + backend from a verified package.
	Decode func(pkg pkgmeta.Package) (config json.RawMessage, backend extreg.Backend, err error)

	// Probe validates a decoded record against its live target before persist
	// (governed by guard: the network allowlist is enforced here).
	Probe func(ctx context.Context, rec Record, guard extgov.Guard) error

	// Activate constructs and registers the live driver for a record, returning
	// the instance stored on the extreg entry.
	Activate func(ctx context.Context, rec Record, guard extgov.Guard) (instance any, err error)

	// Deactivate tears down a live driver on disable/uninstall (optional).
	Deactivate func(ctx context.Context, rec Record) error
}

var installers = map[extreg.Kind]KindInstaller{}

// ErrSettingsStore reports that a pipeline operation required a settings store but none was configured.
var ErrSettingsStore = errors.New("platform settings store not configured")

// ErrTrustedKeys reports failure loading the trusted signing-key set.
var ErrTrustedKeys = errors.New("trusted keys unavailable")

// ErrPersist reports failure writing or deleting an installed extension record.
var ErrPersist = errors.New("extension persistence failed")

// RegisterInstaller registers a kind's lifecycle vtable. Panics on a duplicate,
// matching the driver-registration idiom.
func RegisterInstaller(in KindInstaller) {
	if _, dup := installers[in.Kind]; dup {
		panic("extpipe: duplicate installer for kind " + string(in.Kind))
	}
	installers[in.Kind] = in
}

// Now returns the current time in unix millis; overridable in tests.
var Now = func() int64 { return time.Now().UnixMilli() }

// Pipeline runs the shared lifecycle over a settings store (durable records +
// trusted keys) and an audit store. AllowUnsigned permits installing an
// unsigned package (dev only; loud governance decision at the call site).
type Pipeline struct {
	Settings       store.PlatformSettingsStore
	Audit          store.ExtensionAuditStore
	AllowUnsigned  bool
	Installers     map[extreg.Kind]KindInstaller
	TrustedKeysKey string
	// Now returns unix millis for persisted records and audit entries. Nil uses the package default.
	Now func() int64

	// opMu serializes this process's lifecycle operations (install,
	// uninstall, rollout change, reconcile) so a reconcile never acts on a
	// record list older than an install that just activated. It also guards
	// live and failed. A Pipeline is always used by pointer.
	opMu sync.Mutex
	// live maps recordKey -> the record this process activated, with its
	// fingerprint (see Reconcile). failed remembers the fingerprint whose
	// activation failed so a broken record is not retried every tick.
	live   map[string]liveRecord
	failed map[string]string
}

// Install verifies a signed package (or accepts unsigned when AllowUnsigned),
// decodes it for its kind, probes it, persists the record, activates it unless
// its rollout is off, and audits the transition.
//
// Reinstalling an already-installed name is an upgrade: the operator's
// distribution decisions on the previous record (EnabledTenants, and Rollout
// unless the request names one) carry over. An upgrade that fails its probe
// never touches the previous record; one that fails activation restores and
// re-activates the previous good record, so a bad publish never takes a
// working extension away.
func (p *Pipeline) Install(ctx context.Context, req InstallRequest) (Record, error) {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	if req.Rollout != "" && !ValidRollout(req.Rollout) {
		return Record{}, fmt.Errorf("invalid rollout %q (want auto, optin, or off)", req.Rollout)
	}
	pkg, keyID, contentHash, err := p.verify(ctx, req.Body, req.Signature)
	if err != nil {
		return Record{}, err
	}
	if err := validateArtifactContract(ctx, pkg, req.ArtifactBaseURL, req.ArtifactProbe); err != nil {
		return Record{}, err
	}
	kind, in, ok := p.installerForPackageKind(pkg.Kind)
	if !ok {
		return Record{}, fmt.Errorf("no installer registered for kind %q", pkg.Kind)
	}
	cfg, backend, err := in.Decode(pkg)
	if err != nil {
		return Record{}, fmt.Errorf("decode %s config: %w", pkg.Kind, err)
	}
	prev, upgrading, err := p.load(ctx, kind, pkg.Name)
	if err != nil {
		return Record{}, fmt.Errorf("%w: load previous record: %w", ErrPersist, err)
	}
	rec := Record{
		Kind:          string(kind),
		Name:          pkg.Name,
		Backend:       string(backend),
		Version:       pkg.Version,
		Publisher:     pkg.Publisher,
		KeyID:         keyID,
		ContentHash:   contentHash,
		InstalledAtMs: p.now(),
		Config:        cfg,
		Network:       pkg.Permissions.Network,
		Rollout:       RolloutAuto,
	}
	if upgrading {
		rec.EnabledTenants = prev.EnabledTenants
		rec.Rollout = NormalizeRollout(prev.Rollout)
	}
	if req.Rollout != "" {
		rec.Rollout = NormalizeRollout(req.Rollout)
	}
	guard := p.guard(rec)
	if err := in.Probe(ctx, rec, guard); err != nil {
		p.audit(ctx, rec, "install", wire.AuditOutcomeDeny, req.Actor, err.Error())
		return Record{}, err
	}
	if err := p.save(ctx, rec, req.Actor); err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrPersist, err)
	}
	if upgrading && !rec.active() && prev.active() {
		p.deactivate(ctx, in, prev)
	}
	if rec.active() {
		if err := p.activate(ctx, in, rec, guard); err != nil {
			p.audit(ctx, rec, "install", wire.AuditOutcomeDeny, req.Actor, err.Error())
			if restoreErr := p.restore(ctx, in, rec, prev, upgrading, req.Actor); restoreErr != nil {
				return Record{}, fmt.Errorf("%w: %w (restoring previous record: %w)", ErrPersist, err, restoreErr)
			}
			return Record{}, err
		}
	}
	p.audit(ctx, rec, "install", wire.AuditOutcomeAllow, req.Actor, "")
	return rec, nil
}

// restore puts the pre-install state back after a failed activation: the
// previous good record is re-persisted and re-activated (an upgrade), or the
// new record is removed (a first install).
func (p *Pipeline) restore(ctx context.Context, in KindInstaller, failed, prev Record, upgrading bool, actor string) error {
	if !upgrading {
		return p.Settings.DeletePlatformSetting(ctx, recordKey(extreg.Kind(failed.Kind), failed.Name))
	}
	if err := p.save(ctx, prev, actor); err != nil {
		return err
	}
	if prev.active() {
		if err := p.activate(ctx, in, prev, p.guard(prev)); err != nil {
			p.audit(ctx, prev, "install", wire.AuditOutcomeDeny, actor, "restore previous: "+err.Error())
		}
	}
	return nil
}

// Uninstall deactivates, unregisters, and deletes an extension's record.
func (p *Pipeline) Uninstall(ctx context.Context, kind extreg.Kind, name, actor string) error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	rec, ok, err := p.load(ctx, kind, name)
	if err != nil {
		return err
	}
	if !ok {
		rec = Record{Kind: string(kind), Name: name}
	}
	if err := extreg.Unregister(kind, name); err != nil {
		return err
	}
	if in, ok := p.installer(kind); ok && in.Deactivate != nil {
		_ = in.Deactivate(ctx, rec)
	}
	p.forget(rec)
	if err := p.Settings.DeletePlatformSetting(ctx, recordKey(kind, name)); err != nil {
		return fmt.Errorf("%w: %w", ErrPersist, err)
	}
	p.audit(ctx, rec, "uninstall", wire.AuditOutcomeAllow, actor, "")
	return nil
}

// List returns the persisted records for a kind ("" spans all kinds).
func (p *Pipeline) List(ctx context.Context, kind extreg.Kind) ([]Record, error) {
	if p.Settings == nil {
		return nil, nil
	}
	rows, err := p.Settings.ListPlatformSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, row := range rows {
		if !strings.HasPrefix(row.Key, recordKeyPrefix) || !strings.HasSuffix(row.Key, recordKeySuffix) {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(row.Value), &rec); err != nil {
			kindName := recordKindNameFromKey(row.Key)
			return nil, fmt.Errorf("decode extension record %s (%s): %w", row.Key, kindName, err)
		}
		if kind == "" || rec.Kind == string(kind) {
			rec.Rollout = NormalizeRollout(rec.Rollout)
			out = append(out, rec)
		}
	}
	return out, nil
}

// Rehydrate re-activates every persisted extension whose kind is hosted by this
// service, with the same rule Install and SetRollout apply: every rollout but
// off is active (opt-in records are gated per member at read time, not by
// leaving them dark). Called at boot before the active list is built; it is
// the first Reconcile of the process (nothing is live yet).
// Best-effort per record: one failure is logged via audit, never fatal.
func (p *Pipeline) Rehydrate(ctx context.Context, host extreg.Service) error {
	_, err := p.Reconcile(ctx, host)
	return err
}

func (p *Pipeline) activate(ctx context.Context, in KindInstaller, rec Record, guard extgov.Guard) error {
	instance, err := in.Activate(ctx, rec, guard)
	if err != nil {
		return err
	}
	if instance == nil {
		p.remember(rec, false)
		return nil
	}
	if err := extreg.Reregister(extreg.Extension{
		Kind:     extreg.Kind(rec.Kind),
		Name:     rec.Name,
		Backend:  extreg.Backend(rec.Backend),
		Version:  rec.Version,
		Instance: instance,
	}); err != nil {
		if in.Deactivate != nil {
			_ = in.Deactivate(ctx, rec)
		}
		return err
	}
	p.remember(rec, true)
	return nil
}

// deactivate tears a record's live driver down and drops its registry entry.
func (p *Pipeline) deactivate(ctx context.Context, in KindInstaller, rec Record) {
	if in.Deactivate != nil {
		_ = in.Deactivate(ctx, rec)
	}
	_ = extreg.Unregister(extreg.Kind(rec.Kind), rec.Name)
	p.forget(rec)
}

func (p *Pipeline) guard(rec Record) extgov.Guard {
	return extgov.Guard{Net: extgov.NewNetGuard(rec.Network)}
}
