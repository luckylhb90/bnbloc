package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A channel preset (docs/prd/2026-10-28-channels-and-threads.md §2.1c) is
// saved by form like any content, listed by its kind with its channel setup,
// and refused when its settings are not ones the room service takes.
func TestChannelPresetSaveAndList(t *testing.T) {
	g := newContentGateway(t)
	form := func(settings map[string]any) map[string]any {
		return map[string]any{"form": map[string]any{
			"kind": "channel-preset", "name": "help-desk", "displayName": "Help desk",
			"channel": map[string]any{"purpose": "Ask anything", "rules": "One question per thread.", "settings": settings},
		}}
	}
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components", form(map[string]any{"agentParticipation": wire.ChannelAgentsQuestions, "postingStyle": "threads"}))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	c := decodeDetail(t, raw).Component
	var s map[string]any
	if c.Kind != "channel-preset" || c.Channel == nil || c.Channel.Purpose != "Ask anything" || json.Unmarshal(c.Channel.Settings, &s) != nil || s["postingStyle"] != "threads" {
		t.Fatalf("component = %+v", c)
	}
	_, raw = g.do("maya", http.MethodGet, "/v1/content-components?kind=channel-preset", "", nil)
	var list struct{ Components []wire.ContentComponent }
	_ = json.Unmarshal(raw, &list)
	if len(list.Components) != 1 || list.Components[0].Channel == nil || list.Components[0].Channel.Rules != "One question per thread." {
		t.Fatalf("list = %s", raw)
	}
	_, raw = g.do("maya", http.MethodGet, "/v1/content-components?kind=expert", "", nil)
	_ = json.Unmarshal(raw, &list)
	if len(list.Components) != 0 {
		t.Fatalf("the experts list holds the preset: %s", raw)
	}
	bad := form(map[string]any{"defaultAgent": map[string]any{"ownerId": "maya", "provider": "codex"}})
	bad["form"].(map[string]any)["name"] = "with-agent"
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components", bad); code != http.StatusBadRequest || !strings.Contains(string(raw), "defaultAgent") {
		t.Fatalf("a preset naming a person's agent = %d %s", code, raw)
	}
}
