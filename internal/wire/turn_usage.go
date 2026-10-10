package wire

import (
	"encoding/json"
	"strings"
)

// TurnUsage is the usage one agent reply reports in its free-form metrics map
// (the reply payload's "metrics": model, reported, inputTokens, outputTokens,
// costUsd). It is the ONE reading of that map shared by every consumer — the
// bridge's borrow record and the team dashboard's memory aggregation — so a
// reply counts the same everywhere.
//
// Reported is false when the map is empty or the core marked it
// reported=false (tokens and cost unknown); then the token counts are zero and
// must not be presented as a measured zero. CostUsd is nil unless the reply
// carried a numeric costUsd. Model is read whenever present.
type TurnUsage struct {
	Model        string
	Reported     bool
	InputTokens  int64
	OutputTokens int64
	CostUsd      *float64
}

// TurnUsageFromMetrics reads one reply's metrics map.
func TurnUsageFromMetrics(m map[string]any) TurnUsage {
	var u TurnUsage
	if len(m) == 0 {
		return u
	}
	if s, ok := m["model"].(string); ok {
		u.Model = strings.TrimSpace(s)
	}
	if reported, has := m["reported"].(bool); has && !reported {
		return u
	}
	u.Reported = true
	if v, ok := metricFloat(m["inputTokens"]); ok && v > 0 {
		u.InputTokens = int64(v)
	}
	if v, ok := metricFloat(m["outputTokens"]); ok && v > 0 {
		u.OutputTokens = int64(v)
	}
	if v, ok := metricFloat(m["costUsd"]); ok && v >= 0 {
		u.CostUsd = &v
	}
	return u
}

// metricFloat accepts the numeric shapes a decoded metrics map carries
// (encoding/json float64, json.Number under UseNumber, Go ints from an
// in-process core).
func metricFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
