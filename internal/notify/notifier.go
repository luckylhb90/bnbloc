package notify

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/endpointpkg"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Notifier contract paths, appended to the component's endpoint.baseUrl
// (which may carry a path prefix): GET /discover answers {name, contract};
// POST /v1/notifications receives one signed NotificationPayload.
const (
	DiscoverPath = "/discover"
	DeliveryPath = "/v1/notifications"
)

// Kind is the notifier's endpoint-component description (the shared
// endpoint install path: validate, NetGuard, discover probe, guarded client).
var Kind = endpointpkg.Kind{
	Kind:         extreg.KindNotifier,
	PackageKind:  pkgmeta.KindNotifierPackageV1,
	Label:        "notifier",
	Contract:     pkgmeta.NotifierContractV1,
	DiscoverPath: DiscoverPath,
	Timeout:      deliveryTimeout,
}

// recordsTTL bounds how stale a producer's or worker's view of the notifier
// install records may be: an install, uninstall, rollout or tenant change
// made in another process is seen within it. A chat burst costs one settings
// read, not one per message.
const recordsTTL = 5 * time.Second

// Directory is a service's view of the installed notifier components. The
// install records (platform settings, written by the gateway's pipeline) are
// the source of truth for distribution — rollout and tenant scope are read
// at use time through a short-TTL snapshot. In the delivering service
// (realtime) the component pipeline also activates each record through
// Installer, and the Directory keeps the activated (probed, guarded)
// endpoint; a producer-only service (shareagent) uses just the records.
type Directory struct {
	records func(context.Context) ([]extpipe.Record, error)
	now     func() time.Time

	mu    sync.Mutex
	snap  []extpipe.Record
	at    time.Time
	valid bool
	live  map[string]liveEndpoint
}

type liveEndpoint struct {
	revision string
	endpoint endpointpkg.Endpoint
}

// Target is one notifier component an event of a tenant goes to.
type Target struct {
	Name   string
	OptIn  bool
	Record extpipe.Record
}

// NewDirectory reads the notifier records from the platform settings store.
func NewDirectory(settings store.PlatformSettingsStore) *Directory {
	pipe := &extpipe.Pipeline{Settings: settings}
	return &Directory{records: func(ctx context.Context) ([]extpipe.Record, error) {
		return pipe.List(ctx, extreg.KindNotifier)
	}}
}

func (d *Directory) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// Installer is the notifier lifecycle vtable for the delivering service's
// pipeline: activation probes the endpoint and records it as live here.
func (d *Directory) Installer() extpipe.KindInstaller {
	in := Kind.Installer()
	in.Activate = func(ctx context.Context, rec extpipe.Record, guard extgov.Guard) (any, error) {
		inst, err := Kind.Activate(ctx, rec, guard)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		if d.live == nil {
			d.live = map[string]liveEndpoint{}
		}
		d.live[rec.Name] = liveEndpoint{revision: revision(rec), endpoint: inst.(endpointpkg.Endpoint)}
		d.valid = false // a record changed through this process
		d.mu.Unlock()
		return inst, nil
	}
	in.Deactivate = func(_ context.Context, rec extpipe.Record) error {
		d.mu.Lock()
		delete(d.live, rec.Name)
		d.valid = false
		d.mu.Unlock()
		return nil
	}
	return in
}

func revision(rec extpipe.Record) string {
	return rec.Version + "@" + strconv.FormatInt(rec.InstalledAtMs, 10)
}

// Records returns the notifier install records (snapshot younger than
// recordsTTL, else a fresh read). A failed read returns the error: callers
// deliver nothing (fail closed).
func (d *Directory) Records(ctx context.Context) ([]extpipe.Record, error) {
	if d == nil || d.records == nil {
		return nil, nil
	}
	now := d.clock()
	d.mu.Lock()
	if d.valid && now.Sub(d.at) < recordsTTL {
		recs := d.snap
		d.mu.Unlock()
		return recs, nil
	}
	d.mu.Unlock()
	recs, err := d.records(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.valid = false
		return nil, err
	}
	d.snap, d.at, d.valid = recs, now, true
	return recs, nil
}

// Distributes reports whether rec delivers to tenantID: rollout not off and
// the tenant in scope. Opt-in additionally filters recipients (Target.OptIn).
func Distributes(rec extpipe.Record, tenantID string) bool {
	return extpipe.NormalizeRollout(rec.Rollout) != extpipe.RolloutOff && rec.AllowsTenant(tenantID)
}

// Targets lists the notifier components an event of tenantID goes to.
func (d *Directory) Targets(ctx context.Context, tenantID string) ([]Target, error) {
	recs, err := d.Records(ctx)
	if err != nil {
		return nil, err
	}
	var out []Target
	for _, rec := range recs {
		if Distributes(rec, tenantID) {
			out = append(out, Target{Name: rec.Name, OptIn: extpipe.NormalizeRollout(rec.Rollout) == extpipe.RolloutOptIn, Record: rec})
		}
	}
	return out, nil
}

// Record returns the current install record of a notifier.
func (d *Directory) Record(ctx context.Context, name string) (extpipe.Record, bool, error) {
	recs, err := d.Records(ctx)
	if err != nil {
		return extpipe.Record{}, false, err
	}
	for _, rec := range recs {
		if rec.Name == name {
			return rec, true, nil
		}
	}
	return extpipe.Record{}, false, nil
}

// Live returns the endpoint this process activated for rec's revision.
func (d *Directory) Live(rec extpipe.Record) (endpointpkg.Endpoint, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	l, ok := d.live[rec.Name]
	if !ok || l.revision != revision(rec) {
		return endpointpkg.Endpoint{}, false
	}
	return l.endpoint, true
}
