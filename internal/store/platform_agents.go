package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// PlatformAgentsSettingKey is the platform setting holding the platform-agent
// policy (wire.PlatformAgentsPolicy as JSON: one entry per host).
const PlatformAgentsSettingKey = "platform_agents"

// LoadPlatformAgents reads the platform-agent policy; unset is the empty
// policy (no host publishes anything).
func LoadPlatformAgents(ctx context.Context, st PlatformSettingsStore) (wire.PlatformAgentsPolicy, error) {
	row, ok, err := st.GetPlatformSetting(ctx, PlatformAgentsSettingKey)
	if err != nil {
		return wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{}}, err
	}
	return decodePlatformAgents(row.Value, ok)
}

// decodePlatformAgents parses a stored policy (found=false: the empty one).
func decodePlatformAgents(raw string, found bool) (wire.PlatformAgentsPolicy, error) {
	if !found {
		return wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{}}, nil
	}
	var p wire.PlatformAgentsPolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return wire.PlatformAgentsPolicy{}, fmt.Errorf("platform agents setting is corrupt: %w", err)
	}
	if p.Hosts == nil {
		p.Hosts = []wire.PlatformAgentHost{}
	}
	for i := range p.Hosts {
		if p.Hosts[i].Agents == nil {
			p.Hosts[i].Agents = []wire.PlatformAgentSpec{}
		}
	}
	return p, nil
}

// UpdatePlatformAgents applies change to the stored policy and saves the
// result compare-and-set (UpdatePlatformSetting): when another host saved
// in between, change is re-applied to that save, so two hosts saving their
// entries at once both keep them. It returns the saved policy; a corrupt
// stored policy is an error (nothing is written).
func UpdatePlatformAgents(ctx context.Context, st PlatformSettingsStore, actor string, atMs int64, change func(wire.PlatformAgentsPolicy) wire.PlatformAgentsPolicy) (wire.PlatformAgentsPolicy, error) {
	var saved wire.PlatformAgentsPolicy
	_, err := UpdatePlatformSetting(ctx, st, PlatformAgentsSettingKey, actor, atMs, func(cur string, found bool) (string, error) {
		p, err := decodePlatformAgents(cur, found)
		if err != nil {
			return "", err
		}
		saved = change(p)
		raw, err := json.Marshal(saved)
		return string(raw), err
	})
	if err != nil {
		return wire.PlatformAgentsPolicy{}, err
	}
	return saved, nil
}
