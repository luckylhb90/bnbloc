package gateway

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/buildinfo"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Platform events (docs/prd/2026-10-20-event-triggered-schedules.md): a
// deploy, a component (or platform content) version, a desktop version.
// The gateway is their one owner: recordPlatformEvent records each once
// and hands it to realtime's scheduler, which runs the schedules following
// it. Every producer calls it:
//
//   - platform.deployed: RecordDeploy, at boot, with the build's commit;
//   - component.published: componentPublished, from the extension install
//     (handleInstallComponent), platform content (installPlatformContent)
//     and an organization's publish (orgPublish, that organization only);
//   - desktop.published: handlePlatformUpdateConfigPut, when the updater
//     serves a version.
//
// "Once" is a platform setting per event and subject (eventMarkKey) moved
// with store.MovePlatformSetting: of several replicas booting one build,
// or two installs of one package, only the one that moves the mark
// dispatches, and a mark that is already there (the same version and
// bytes published again) dispatches nothing.

// eventMarkPrefix opens every platform event's mark key.
const eventMarkPrefix = "events."

// eventActor records who moved an event mark.
const eventActor = "bnbloc-gateway"

// A recorded event's hand-off to realtime is retried: the first wait is
// platformEventRetry, doubling up to platformEventRetryMax, for
// platformEventAttempts hand-offs (a deploy restarts realtime too, which
// may come up after the gateway).
const (
	platformEventRetry    = 2 * time.Second
	platformEventRetryMax = time.Minute
	platformEventAttempts = 20
)

// eventMarkKey is the platform setting holding the last recorded mark of
// event for subject ("" for an event with one subject: the deploy, the
// desktop version).
func eventMarkKey(event, subject string) string {
	if subject == "" {
		return eventMarkPrefix + event
	}
	return eventMarkPrefix + event + "." + subject
}

// recordPlatformEvent records ev under subject when its mark differs from
// the last one recorded (same compares marks; nil is equality), then hands
// it to realtime in the background. It reports whether this call recorded
// it.
func (s *Service) recordPlatformEvent(ctx context.Context, ev wire.PlatformEvent, subject, mark string, same func(a, b string) bool) bool {
	if s.PlatformSettings == nil || strings.TrimSpace(mark) == "" {
		return false
	}
	if same == nil {
		same = func(a, b string) bool { return a == b }
	}
	now := s.nowTime()
	prev, moved, err := store.MovePlatformSetting(ctx, s.PlatformSettings, eventMarkKey(ev.Event, subject), mark, eventActor, now.UnixMilli(), same)
	if err != nil {
		slog.Warn("bnbloc-gateway: record platform event failed", "event", ev.Event, "subject", subject, "error", err)
		return false
	}
	if !moved {
		return false
	}
	if ev.AtMs <= 0 {
		ev.AtMs = now.UnixMilli()
	}
	if ev.Event == wire.PlatformDeployed {
		ev.Previous = prev
	}
	go s.dispatchPlatformEvent(context.WithoutCancel(ctx), ev)
	return true
}

// dispatchPlatformEvent hands ev to realtime, retrying with backoff: the
// event is recorded once, so a hand-off that never lands is lost (logged).
func (s *Service) dispatchPlatformEvent(ctx context.Context, ev wire.PlatformEvent) {
	line := schedule.EventLine(ev)
	if s.Backends.Realtime == "" {
		slog.Warn("bnbloc-gateway: platform event not dispatched: no realtime", "event", line)
		return
	}
	pub := realtime.NewHTTPPublisher(s.Backends.Realtime, "gateway")
	wait := cmp.Or(s.eventRetry, platformEventRetry)
	for attempt := 1; ; attempt++ {
		err := pub.PlatformEvent(ctx, ev)
		if err == nil {
			slog.Info("bnbloc-gateway: platform event dispatched", "event", line)
			return
		}
		if attempt >= platformEventAttempts {
			slog.Error("bnbloc-gateway: platform event lost: realtime never took it", "event", line, "attempts", attempt, "error", err)
			return
		}
		slog.Warn("bnbloc-gateway: platform event hand-off failed, retrying", "event", line, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, platformEventRetryMax)
	}
}

// RecordDeploy records platform.deployed when this gateway runs a build
// whose version (its commit, buildinfo) differs from the last one
// recorded; every replica calls it at boot and one records it (it reports
// whether this call did). An unstamped build (no commit) records nothing.
func (s *Service) RecordDeploy(ctx context.Context, version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		slog.Warn("bnbloc-gateway: build has no commit stamp; platform.deployed is not recorded")
		return false
	}
	return s.recordPlatformEvent(ctx, wire.PlatformEvent{Event: wire.PlatformDeployed, Version: version}, "", version, buildinfo.SameCommit)
}

// eventScopePlatform is where the mark of a component published for the
// platform lives (an organization's own publish uses its id).
const eventScopePlatform = "platform"

// componentPublished records component.published for a component version:
// kind and name say which, mark the version and its bytes (a version
// published again with the same bytes is not new), tenants the
// organizations it reaches (none: every one) and scope where its mark
// lives ("platform", or the organization of its own publish).
func (s *Service) componentPublished(ctx context.Context, scope, kind, name, version, digest, rollout string, tenants []string) {
	ev := wire.PlatformEvent{Event: wire.ComponentPublished, Kind: kind, Name: name, Version: version, Rollout: rollout, Tenants: slices.Clone(tenants)}
	s.recordPlatformEvent(ctx, ev, scope+"."+kind+"."+name, version+"@"+digest, nil)
}

// orgPublished records component.published for a version an organization
// published of its member's component (orgPublish): only that
// organization's schedules see it. Publishing a version the organization
// already uses again records nothing.
func (s *Service) orgPublished(ctx context.Context, row store.ContentComponent, v store.ContentComponentVersion) {
	s.componentPublished(ctx, row.TenantID, shortKind(row.Kind), row.Name, row.LatestVersion, v.SHA256, "", []string{row.TenantID})
}

// desktopPublished records desktop.published when the updater serves a
// version (updates on, a version named) for the targets it has files for.
func (s *Service) desktopPublished(ctx context.Context, cfg wire.UpdateConfig) {
	if !cfg.Policy.Enabled || strings.TrimSpace(cfg.Release.Version) == "" {
		return
	}
	var targets []string
	for _, t := range wire.UpdateTargets {
		if p, ok := cfg.Release.Platforms[t]; ok && p.URL != "" {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return
	}
	ev := wire.PlatformEvent{Event: wire.DesktopPublished, Version: cfg.Release.Version, Targets: targets}
	s.recordPlatformEvent(ctx, ev, "", cfg.Release.Version, nil)
}
