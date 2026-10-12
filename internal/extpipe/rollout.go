package extpipe

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const (
	RolloutAuto  = "auto"
	RolloutOptIn = "optin"
	RolloutOff   = "off"
)

type recordJSON Record

func (r *Record) UnmarshalJSON(data []byte) error {
	var raw recordJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Rollout != "" && !ValidRollout(raw.Rollout) {
		return fmt.Errorf("invalid rollout %q (want auto, optin, or off)", raw.Rollout)
	}
	*r = Record(raw)
	r.Rollout = NormalizeRollout(r.Rollout)
	return nil
}

func NormalizeRollout(v string) string {
	switch v {
	case RolloutOptIn, RolloutOff:
		return v
	default:
		return RolloutAuto
	}
}

func ValidRollout(v string) bool {
	return v == RolloutAuto || v == RolloutOptIn || v == RolloutOff
}

// SetRollout changes an installed extension's distribution policy. Moving to
// off deactivates the driver; moving to auto or optin (re)activates it.
func (p *Pipeline) SetRollout(ctx context.Context, kind extreg.Kind, name, rollout, actor string) error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	if !ValidRollout(rollout) {
		return fmt.Errorf("invalid rollout %q (want auto, optin, or off)", rollout)
	}
	rec, ok, err := p.load(ctx, kind, name)
	if err != nil || !ok {
		return notFound(err, kind, name)
	}
	in, hasInstaller := p.installer(kind)
	if hasInstaller {
		p.deactivate(ctx, in, rec)
	} else {
		_ = extreg.Unregister(kind, name)
	}
	rec.Rollout = rollout
	if err := p.save(ctx, rec, actor); err != nil {
		return err
	}
	if hasInstaller && rec.active() {
		if err := p.activate(ctx, in, rec, p.guard(rec)); err != nil {
			p.audit(ctx, rec, "set-rollout", wire.AuditOutcomeDeny, actor, err.Error())
			return err
		}
	}
	p.audit(ctx, rec, "set-rollout", wire.AuditOutcomeAllow, actor, rollout)
	return nil
}
