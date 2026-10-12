package gateway

import (
	"context"
	"fmt"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const (
	updateKeyEnabled             = "updates.enabled"
	updateKeyChannel             = "updates.channel"
	updateKeyAutoCheck           = "updates.auto_check"
	updateKeyCheckIntervalHours  = "updates.check_interval_hours"
	updateKeyAutoInstall         = "updates.auto_install"
	updateKeyMinSupportedVersion = "updates.min_supported_version"
	updateKeyReleaseVersion      = "release.version"
	updateKeyReleaseNotes        = "release.notes"
	updateKeyReleasePubDate      = "release.pub_date"
)

func defaultUpdatePolicy() wire.UpdatePolicy {
	return wire.UpdatePolicy{Channel: wire.UpdateChannelStable, AutoCheck: true, CheckIntervalHours: 24}
}

func loadUpdateConfig(ctx context.Context, settings store.PlatformSettingsStore) (wire.UpdateConfig, error) {
	cfg := wire.UpdateConfig{Policy: defaultUpdatePolicy(), Release: wire.UpdateRelease{Platforms: map[string]wire.UpdatePlatform{}}}
	if settings == nil {
		return cfg, nil
	}
	rows, err := settings.ListPlatformSettings(ctx)
	if err != nil {
		return wire.UpdateConfig{}, err
	}
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	cfg.Policy.Enabled = parseBoolSetting(values[updateKeyEnabled], cfg.Policy.Enabled)
	cfg.Policy.Channel = strings.TrimSpace(values[updateKeyChannel])
	if cfg.Policy.Channel == "" {
		cfg.Policy.Channel = wire.UpdateChannelStable
	}
	cfg.Policy.AutoCheck = parseBoolSetting(values[updateKeyAutoCheck], cfg.Policy.AutoCheck)
	cfg.Policy.CheckIntervalHours = parseIntSetting(values[updateKeyCheckIntervalHours], cfg.Policy.CheckIntervalHours)
	cfg.Policy.AutoInstall = parseBoolSetting(values[updateKeyAutoInstall], cfg.Policy.AutoInstall)
	cfg.Policy.MinSupportedVersion = strings.TrimSpace(values[updateKeyMinSupportedVersion])
	cfg.Release.Version = strings.TrimSpace(values[updateKeyReleaseVersion])
	cfg.Release.Notes = values[updateKeyReleaseNotes]
	cfg.Release.PubDate = strings.TrimSpace(values[updateKeyReleasePubDate])
	for _, target := range wire.UpdateTargets {
		p := wire.UpdatePlatform{
			URL:       strings.TrimSpace(values[releaseTargetKey(target, "url")]),
			Signature: strings.TrimSpace(values[releaseTargetKey(target, "signature")]),
		}
		if p.URL != "" || p.Signature != "" {
			cfg.Release.Platforms[target] = p
		}
	}
	return cfg, nil
}

type updateConfigSave struct {
	Config      wire.UpdateConfig
	UpdatedBy   string
	UpdatedAtMs int64
}

func saveUpdateConfig(ctx context.Context, settings store.PlatformSettingsStore, save updateConfigSave) error {
	if settings == nil {
		return fmt.Errorf("platform settings store not configured")
	}
	entries := map[string]string{
		updateKeyEnabled:             strconv.FormatBool(save.Config.Policy.Enabled),
		updateKeyChannel:             save.Config.Policy.Channel,
		updateKeyAutoCheck:           strconv.FormatBool(save.Config.Policy.AutoCheck),
		updateKeyCheckIntervalHours:  strconv.Itoa(save.Config.Policy.CheckIntervalHours),
		updateKeyAutoInstall:         strconv.FormatBool(save.Config.Policy.AutoInstall),
		updateKeyMinSupportedVersion: save.Config.Policy.MinSupportedVersion,
		updateKeyReleaseVersion:      save.Config.Release.Version,
		updateKeyReleaseNotes:        save.Config.Release.Notes,
		updateKeyReleasePubDate:      save.Config.Release.PubDate,
	}
	for target, p := range save.Config.Release.Platforms {
		entries[releaseTargetKey(target, "url")] = p.URL
		entries[releaseTargetKey(target, "signature")] = p.Signature
	}
	for _, target := range wire.UpdateTargets {
		if _, ok := save.Config.Release.Platforms[target]; ok {
			continue
		}
		entries[releaseTargetKey(target, "url")] = ""
		entries[releaseTargetKey(target, "signature")] = ""
	}
	for key, value := range entries {
		setting := store.PlatformSetting{Key: key, Value: value, UpdatedAtMs: save.UpdatedAtMs, UpdatedBy: save.UpdatedBy}
		if err := settings.PutPlatformSetting(ctx, setting); err != nil {
			return err
		}
	}
	return nil
}

// maxCheckIntervalHours caps the desktop update-check interval at 30 days.
// Clients schedule the check with a 32-bit millisecond timer (JS setInterval,
// max ~24.8 days ≈ 596 h); a larger value overflows into a tight check loop,
// so the server never publishes one beyond a sane bound.
const maxCheckIntervalHours = 24 * 30

func validateUpdateConfig(cfg wire.UpdateConfig) error {
	if cfg.Policy.Channel != wire.UpdateChannelStable && cfg.Policy.Channel != wire.UpdateChannelInternal {
		return fmt.Errorf("updates.channel must be stable or internal")
	}
	if cfg.Policy.CheckIntervalHours < 1 || cfg.Policy.CheckIntervalHours > maxCheckIntervalHours {
		return fmt.Errorf("updates.check_interval_hours must be between 1 and %d", maxCheckIntervalHours)
	}
	if cfg.Policy.MinSupportedVersion != "" {
		if _, err := semver.Parse(cfg.Policy.MinSupportedVersion); err != nil {
			return fmt.Errorf("updates.min_supported_version must be valid semver")
		}
	}
	if cfg.Release.Version != "" {
		if _, err := semver.Parse(cfg.Release.Version); err != nil {
			return fmt.Errorf("release.version must be valid semver")
		}
	}
	if cfg.Release.PubDate != "" {
		if _, err := time.Parse(time.RFC3339, cfg.Release.PubDate); err != nil {
			return fmt.Errorf("release.pub_date must be RFC3339")
		}
	}
	for target, p := range cfg.Release.Platforms {
		if !wire.IsUpdateTarget(target) {
			return fmt.Errorf("unsupported release target %q", target)
		}
		if p.URL != "" {
			if err := validateHTTPURL(p.URL); err != nil {
				return fmt.Errorf("release.%s.url must be an absolute http(s) URL", target)
			}
		}
		if p.URL == "" && p.Signature != "" || p.URL != "" && p.Signature == "" {
			return fmt.Errorf("release.%s.url and release.%s.signature must be set together", target, target)
		}
	}
	return nil
}

func releaseTargetKey(target, field string) string {
	return "release." + target + "." + field
}

func parseBoolSetting(raw string, fallback bool) bool {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseIntSetting(raw string, fallback int) int {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return parsed
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("not absolute http(s)")
	}
	return nil
}

func requestUpdateChannel(r *http.Request) string {
	channel := strings.TrimSpace(r.Header.Get("X-Bnbloc-Update-Channel"))
	if channel == "" {
		channel = strings.TrimSpace(r.URL.Query().Get("channel"))
	}
	if channel == "" {
		return wire.UpdateChannelStable
	}
	return channel
}

type updateRequest struct {
	Target         string
	CurrentVersion string
	ClientChannel  string
}
