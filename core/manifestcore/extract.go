package manifestcore

import (
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
)

func idleFromPredicate(doc any, pred *IdlePredicate) bool {
	if pred == nil {
		return true
	}
	if pred.UnlessSet != "" && jsonPath(doc, pred.UnlessSet) != nil {
		return true
	}
	val := jsonPathString(doc, pred.Field)
	for _, bad := range pred.NotIn {
		if val == bad {
			return false
		}
	}
	return true
}

// extractString resolves the dotted JSON path mapped to key in op.Extract.
func (op Op) extractString(doc any, key string) string {
	return jsonPathString(doc, op.extractPath(key))
}

func (op Op) extractPath(key string) string {
	if op.Extract == nil {
		return ""
	}
	path, ok := op.Extract[key]
	if !ok {
		return ""
	}
	return path
}

// extractTargets shapes a list response into core.Target values per op.Targets.
func (op Op) extractTargets(doc any) []core.Target {
	if op.Targets == nil {
		return nil
	}
	list := jsonPath(doc, op.Targets.List)
	arr, ok := list.([]any)
	if !ok {
		return nil
	}
	out := make([]core.Target, 0, len(arr))
	for _, el := range arr {
		if !equalsPredicate(el, op.Targets.EnabledWhen) {
			continue
		}
		id := jsonPathString(el, op.Targets.ID)
		if id == "" {
			continue
		}
		out = append(out, core.Target{
			ID:               id,
			Provider:         jsonPathString(el, op.Targets.Provider),
			Models:           stringSliceFromPath(el, "models"),
			ReasoningEfforts: stringSliceFromPath(el, "reasoningEfforts"),
			ReasoningByModel: stringSliceMapFromPath(el, "reasoningByModel"),
			DefaultModel:     jsonPathString(el, "defaultModel"),
			DefaultReasoning: jsonPathString(el, "defaultReasoning"),
		})
	}
	return out
}

// itemList resolves the op's items.list array, or nil when the op has no items
// spec or the path does not hold an array.
func (op Op) itemList(doc any) []any {
	if op.Items == nil {
		return nil
	}
	arr, _ := jsonPath(doc, op.Items.List).([]any)
	return arr
}

// extractFiles shapes a listSessionFiles response into session files per
// op.Items; elements with an empty path are dropped.
func (op Op) extractFiles(doc any) []core.SessionFile {
	arr := op.itemList(doc)
	out := make([]core.SessionFile, 0, len(arr))
	for _, el := range arr {
		path := jsonPathString(el, op.Items.Fields["path"])
		if path == "" {
			continue
		}
		out = append(out, core.SessionFile{
			Path:    path,
			Bytes:   int64FromPath(el, op.Items.Fields["bytes"]),
			MtimeMs: int64FromPath(el, op.Items.Fields["mtimeMs"]),
		})
	}
	return out
}

func (op Op) extractMessages(doc any) []core.Message {
	arr := op.itemList(doc)
	if arr == nil {
		return nil
	}
	out := make([]core.Message, 0, len(arr))
	for _, el := range arr {
		out = append(out, core.Message{
			Version:     int64FromPath(el, op.Items.Fields["version"]),
			Role:        jsonPathString(el, op.Items.Fields["role"]),
			Text:        jsonPathString(el, op.Items.Fields["text"]),
			UserVisible: boolFromPath(el, op.Items.Fields["userVisible"]),
			Completed:   completedFromPredicate(el, op.Items.CompletedWhen),
			MessageID:   jsonPathString(el, op.Items.Fields["messageId"]),
			Seq:         int64FromPath(el, op.Items.Fields["seq"]),
			CreatedAtMs: int64FromPath(el, op.Items.Fields["createdAtMs"]),
			Thinking:    jsonPathString(el, op.Items.Fields["thinking"]),
			Metrics:     mapFromPath(el, op.Items.Fields["metrics"]),
		})
	}
	return out
}

func completedFromPredicate(doc any, pred *EqualsAnyPredicate) bool {
	if pred == nil {
		return true
	}
	val := jsonPathString(doc, pred.Field)
	for _, want := range pred.EqualsAnyOf {
		if strings.EqualFold(val, want) {
			return true
		}
	}
	return false
}

// equalsPredicate reports whether the element's field equals the declared
// value; a nil predicate always holds. Both sides are stringified, so a
// manifest may compare against true/false or numbers.
func equalsPredicate(doc any, pred *EqualsPredicate) bool {
	if pred == nil {
		return true
	}
	return jsonPathString(doc, pred.Field) == anyToString(pred.Equals)
}

func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return ""
	}
}

// firstNonEmptyPath resolves pipe-separated fallback paths and returns the
// first non-empty leaf ("effectiveSettings.model|modelConfig.currentValue").
func firstNonEmptyPath(doc any, paths string) string {
	for _, path := range strings.Split(paths, "|") {
		if s := jsonPathString(doc, strings.TrimSpace(path)); s != "" {
			return s
		}
	}
	return ""
}

// listProjection extracts a []string from the array of objects at proj.Path,
// taking proj.Value from each element and dropping empties. proj.When gates
// the whole list (e.g. models are offered only when configurable is true).
func listProjection(doc any, proj ListProj) []string {
	if !equalsPredicate(doc, proj.When) {
		return nil
	}
	arr, ok := jsonPath(doc, proj.Path).([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, el := range arr {
		if s := jsonPathString(el, proj.Value); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// mapProjection extracts map[string][]string from the object at proj.Path:
// each value is an object whose proj.List field holds the array, and proj.Value
// names each element's string field. Keys with no non-empty values are
// dropped, so the map never carries empty ladders.
func mapProjection(doc any, proj MapProj) map[string][]string {
	m, ok := jsonPath(doc, proj.Path).(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string][]string, len(m))
	for key, raw := range m {
		val, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		arr, ok := val[proj.List].([]any)
		if !ok {
			continue
		}
		var vals []string
		for _, el := range arr {
			if s := jsonPathString(el, proj.Value); s != "" {
				vals = append(vals, s)
			}
		}
		if len(vals) > 0 {
			out[key] = vals
		}
	}
	return out
}
