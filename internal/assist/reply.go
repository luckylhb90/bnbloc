package assist

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Bounds of a JSON reply's values.
const (
	maxFieldRunes = 2000
	maxListItems  = 30
)

// Fields holds a JSON task's reply to its declared fields: the object (a
// fenced ```json block or text around it tolerated), each declared field a
// string (a list field: a list of strings; a string is split at commas),
// unknown keys dropped. ErrBadReply when it is not an object or holds none
// of the fields.
func Fields(reply string, fields []wire.AssistField) (map[string]any, error) {
	obj, ok := object(reply)
	if !ok {
		return nil, ErrBadReply
	}
	out := map[string]any{}
	for _, f := range fields {
		v, ok := obj[f.Name]
		if !ok || v == nil {
			continue
		}
		if f.List {
			if l := list(v); len(l) > 0 {
				out[f.Name] = l
			}
			continue
		}
		if s := scalar(v); s != "" {
			out[f.Name] = cut(s, maxFieldRunes)
		}
	}
	if len(out) == 0 {
		return nil, ErrBadReply
	}
	return out, nil
}

// object finds the reply's JSON object: the whole text, a fenced block, or
// the outermost braces.
func object(reply string) (map[string]any, bool) {
	s := strings.TrimSpace(reply)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, "json"), "JSON")
		if j := strings.Index(rest, "```"); j >= 0 {
			s = strings.TrimSpace(rest[:j])
		}
	}
	var obj map[string]any
	if json.Unmarshal([]byte(s), &obj) == nil {
		return obj, true
	}
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return nil, false
	}
	if json.Unmarshal([]byte(s[i:j+1]), &obj) != nil {
		return nil, false
	}
	return obj, true
}

func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64, bool:
		return fmt.Sprint(t)
	}
	return ""
}

func list(v any) []string {
	var raw []string
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			raw = append(raw, scalar(e))
		}
	case string:
		raw = strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == '，' || r == '、' || r == '\n' })
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range raw {
		s = cut(s, maxFieldRunes)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == maxListItems {
			break
		}
	}
	return out
}
