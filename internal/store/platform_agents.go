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
	if err != nil || !ok {
		return wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{}}, err
	}
	var p wire.PlatformAgentsPolicy
	if err := json.Unmarshal([]byte(row.Value), &p); err != nil {
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

// SavePlatformAgents persists the platform-agent policy.
func SavePlatformAgents(ctx context.Context, st PlatformSettingsStore, p wire.PlatformAgentsPolicy, actor string, atMs int64) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return st.PutPlatformSetting(ctx, PlatformSetting{Key: PlatformAgentsSettingKey, Value: string(raw), UpdatedAtMs: atMs, UpdatedBy: actor})
}
