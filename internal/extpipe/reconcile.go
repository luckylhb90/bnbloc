package extpipe

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// liveRecord is a record this process activated, with the fingerprint of the
// version that is live.
type liveRecord struct {
	rec         Record
	fingerprint string
	// registered is true when activation put an instance in the registry;
	// such a record is only live while that entry is still there.
	registered bool
}

// current reports whether the live record still matches fp and, when it
// registered an instance, that the registry still holds it.
func (l liveRecord) current(fp string) bool {
	if l.fingerprint != fp {
		return false
	}
	if l.registered {
		_, ok := extreg.Get(extreg.Kind(l.rec.Kind), l.rec.Name)
		return ok
	}
	return true
}

// fingerprint identifies the driver-relevant content of a record: a change
// in any of these needs a re-activation. Distribution filters (rollout
// auto/optin, tenant scope) are read-time gates and deliberately excluded.
func fingerprint(rec Record) string {
	return strings.Join([]string{
		rec.Backend, rec.Version, rec.ContentHash,
		strconv.FormatInt(rec.InstalledAtMs, 10),
		strings.Join(rec.Network, ","),
	}, "\x00")
}

// ReconcileReport lists what one Reconcile changed, as "kind/name".
type ReconcileReport struct {
	Activated   []string
	Deactivated []string
	Failed      []string
}

// Changed reports whether the reconcile did anything worth logging.
func (r ReconcileReport) Changed() bool {
	return len(r.Activated)+len(r.Deactivated)+len(r.Failed) > 0
}

// Reconcile converges this process's live extensions on the durable records:
// it activates every active record hosted by host that is not live (or whose
// content changed), and deactivates every live record that was uninstalled
// or switched off. Records are the shared source of truth, so on a
// multi-replica deployment an install, upgrade, rollout change or uninstall
// on one replica reaches every other replica at its next reconcile. Boot is
// the first reconcile (Rehydrate).
//
// Idempotent and cheap when nothing changed (one settings read). Best-effort
// per record: a failed activation is audited once per fingerprint and not
// retried until the record changes.
func (p *Pipeline) Reconcile(ctx context.Context, host extreg.Service) (ReconcileReport, error) {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	var report ReconcileReport
	recs, err := p.List(ctx, "")
	if err != nil {
		return report, err
	}
	desired := map[string]Record{}
	for _, rec := range recs {
		kind := extreg.Kind(rec.Kind)
		desc, ok := extreg.Descriptor(kind)
		if !ok || !hostsInclude(desc.Hosts, host) {
			continue
		}
		if _, ok := p.installer(kind); !ok || !rec.active() {
			continue
		}
		desired[recordKey(kind, rec.Name)] = rec
	}
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rec := desired[key]
		fp := fingerprint(rec)
		if live, ok := p.live[key]; ok && live.current(fp) {
			continue
		}
		if p.failed[key] == fp {
			continue
		}
		in, _ := p.installer(extreg.Kind(rec.Kind))
		if err := p.activate(ctx, in, rec, p.guard(rec)); err != nil {
			p.audit(ctx, rec, "enable", wire.AuditOutcomeDeny, "", "reconcile: "+err.Error())
			if p.failed == nil {
				p.failed = map[string]string{}
			}
			p.failed[key] = fp
			report.Failed = append(report.Failed, rec.Kind+"/"+rec.Name)
			continue
		}
		delete(p.failed, key)
		report.Activated = append(report.Activated, rec.Kind+"/"+rec.Name)
	}
	var gone []string
	for key := range p.live {
		if _, ok := desired[key]; !ok {
			gone = append(gone, key)
		}
	}
	sort.Strings(gone)
	for _, key := range gone {
		rec := p.live[key].rec
		if in, ok := p.installer(extreg.Kind(rec.Kind)); ok {
			p.deactivate(ctx, in, rec)
		} else {
			_ = extreg.Unregister(extreg.Kind(rec.Kind), rec.Name)
			p.forget(rec)
		}
		report.Deactivated = append(report.Deactivated, rec.Kind+"/"+rec.Name)
	}
	for key := range p.failed {
		if _, ok := desired[key]; !ok {
			delete(p.failed, key)
		}
	}
	return report, nil
}

// remember records rec as live in this process (registered: activation put
// an instance in the registry). Callers hold opMu.
func (p *Pipeline) remember(rec Record, registered bool) {
	if p.live == nil {
		p.live = map[string]liveRecord{}
	}
	p.live[recordKey(extreg.Kind(rec.Kind), rec.Name)] = liveRecord{rec: rec, fingerprint: fingerprint(rec), registered: registered}
}

// forget drops rec from the live set. Callers hold opMu.
func (p *Pipeline) forget(rec Record) {
	delete(p.live, recordKey(extreg.Kind(rec.Kind), rec.Name))
}

// ReconcileInterval is how often every replica of a hosting service re-reads
// the installed component records and converges its live drivers on them.
// Installs happen on one replica (the gateway); every other process picks
// them up within one interval. A protocol constant of the multi-replica
// design, not deployment data: it bounds propagation delay, and an idle
// reconcile is one settings read.
const ReconcileInterval = 30 * time.Second

// RunReconcile reconciles host's components every interval until ctx ends.
// Boot already ran the first reconcile (Rehydrate), so the loop starts with
// a wait. service names the process in log lines.
func (p *Pipeline) RunReconcile(ctx context.Context, host extreg.Service, interval time.Duration, service string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.ReconcileLogged(ctx, host, service)
		}
	}
}

// ReconcileLogged runs one Reconcile and logs any change or error.
func (p *Pipeline) ReconcileLogged(ctx context.Context, host extreg.Service, service string) {
	if p.Settings == nil {
		return
	}
	report, err := p.Reconcile(ctx, host)
	if err != nil {
		log.Printf("%s: component reconcile: %v", service, err)
		return
	}
	if report.Changed() {
		log.Printf("%s: component reconcile: activated %v, deactivated %v, failed %v", service, report.Activated, report.Deactivated, report.Failed)
	}
}
