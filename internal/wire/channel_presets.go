package wire

import "encoding/json"

// Channel presets (docs/prd/2026-10-28-channels-and-threads.md §2.1c): a
// kind of channel saved as a content component (contentpkg.KindChannelPreset,
// its manifest checked by contentpkg) and applied to a channel by one PATCH
// of the room service's channel route (ChannelPatchRequest). The two meet at
// the settings keys: Settings is the route's settings document (ChannelSettings,
// by key), so a key the room service learns takes effect without a change here.

// ChannelPreset is a preset's channel setup: the purpose and rules it gives
// the channel and its settings document (JSON, ChannelSettings' keys without
// a default agent: a preset names no people). Empty fields are left as the
// channel has them.
type ChannelPreset struct {
	Purpose  string          `json:"purpose,omitempty"`
	Rules    string          `json:"rules,omitempty"`
	Settings json.RawMessage `json:"settings,omitempty"`
}
