package manifestcore

import (
	"encoding/json"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// renderTemplate substitutes {var} placeholders in a string with values from v,
// verbatim (body string leaves). An unknown placeholder renders empty. The
// {name|firstTarget} form is treated as {name} here; the firstTarget fallback
// is resolved by the caller before rendering (it needs a network call).
func renderTemplate(s string, v vars) string { return render(s, v, false) }

// renderPath substitutes {var} placeholders in a request path template and
// escapes each value for where it lands: before the template's first literal
// '?' a value is path-escaped (a '/' or '?' in an id cannot split or end a
// segment), after it query-escaped (a file name carrying '&', '#', '+' or a
// space cannot change which parameter the daemon reads). Template literals are
// never escaped — they are the manifest author's URL.
func renderPath(s string, v vars) string { return render(s, v, true) }

func render(s string, v vars, escape bool) string {
	var b strings.Builder
	inQuery := false
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			b.WriteString(s)
			break
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		if strings.IndexByte(s[:i], '?') >= 0 {
			inQuery = true
		}
		name := s[i+1 : i+j]
		if k := strings.IndexByte(name, '|'); k >= 0 {
			name = name[:k]
		}
		val := v[strings.TrimSpace(name)]
		if escape {
			if inQuery {
				val = url.QueryEscape(val)
			} else {
				val = url.PathEscape(val)
			}
		}
		b.WriteString(val)
		s = s[i+j+1:]
	}
	return b.String()
}

// pathWhenTemplate returns the conditional path fragments whose var is set
// (non-zero), in name order and unrendered, so the caller renders them with the
// base path in one renderPath pass (a fragment after the base's '?' is in query
// position).
func pathWhenTemplate(pathWhen map[string]string, v vars) string {
	var b strings.Builder
	names := make([]string, 0, len(pathWhen))
	for name := range pathWhen {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if isZeroValue(v[name]) {
			continue
		}
		b.WriteString(pathWhen[name])
	}
	return b.String()
}

func isZeroValue(v string) bool {
	return strings.TrimSpace(v) == "" || strings.TrimSpace(v) == "0"
}

// omitKey is the sentinel a "{var|omit}" leaf resolves to when the var is
// empty; the parent map drops keys carrying it, and drops itself when every
// leaf it had was dropped. This keeps a request body free of empty-string
// fields some daemons reject (e.g. an agentOptions object).
const omitKey = "\x00manifestcore-omit\x00"

// omitLeaf resolves the exact form "{var|omit}": the var's value, or the omit
// sentinel when the var is empty/zero. ok=false for every other string.
func omitLeaf(s string, v vars) (string, bool) {
	if len(s) < 9 || s[0] != '{' || s[len(s)-1] != '}' {
		return "", false
	}
	name := s[1 : len(s)-1]
	k := strings.IndexByte(name, '|')
	if k < 0 || name[k+1:] != "omit" {
		return "", false
	}
	name = name[:k]
	if isZeroValue(v[name]) {
		return omitKey, true
	}
	return v[name], true
}

// renderJSON walks a JSON body template and substitutes {var} placeholders that
// appear as whole string values (e.g. "project_id": "{workspaceId}"). Nested
// objects/arrays are walked recursively; non-string leaves pass through. The
// result is re-marshaled to bytes for the request.
func renderJSON(raw json.RawMessage, v vars) []byte {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	out := substitute(doc, v)
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return b
}

// substitute replaces {var} placeholders inside string leaves of a decoded JSON
// value. A string that is exactly one placeholder is replaced by the value; a
// string with embedded placeholders is rendered inline. The exact form
// "{var|omit}" deletes the key when the var is empty.
func substitute(doc any, v vars) any {
	switch t := doc.(type) {
	case string:
		if s, ok := omitLeaf(t, v); ok {
			return s
		}
		return renderTemplate(t, v)
	case map[string]any:
		hadEntries := len(t) > 0
		out := make(map[string]any, len(t))
		for k, val := range t {
			sub := substitute(val, v)
			if s, ok := sub.(string); ok && s == omitKey {
				continue
			}
			out[k] = sub
		}
		if hadEntries && len(out) == 0 {
			return omitKey
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = substitute(val, v)
		}
		return out
	default:
		return doc
	}
}

// bodyWantsFirstTarget reports whether the body template references the
// {targetId|firstTarget} fallback form, so the caller resolves the first target
// when the request omits an explicit one.
func bodyWantsFirstTarget(raw json.RawMessage) bool {
	return strings.Contains(string(raw), "|firstTarget")
}

// jsonPath resolves a dotted path against a decoded JSON value. Supports object
// keys ("a.b") and the "[]" list marker only as a trailing/segment indicator
// for extractTargets (which passes the list path directly). Returns nil when the
// path does not resolve.
func jsonPath(doc any, path string) any {
	if path == "" {
		return doc
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		seg = strings.TrimSuffix(seg, "[]")
		if seg == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	return cur
}

// jsonPathString resolves a dotted path and coerces the leaf to a string
// (numbers and bools are stringified) so extraction is forgiving of JSON types.
func jsonPathString(doc any, path string) string {
	switch t := jsonPath(doc, path).(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return ""
	}
}

func int64FromPath(doc any, path string) int64 {
	switch t := jsonPath(doc, path).(type) {
	case float64:
		if math.Trunc(t) == t {
			return int64(t)
		}
		return 0
	case string:
		v, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0
		}
		return v
	default:
		return 0
	}
}

func boolFromPath(doc any, path string) bool {
	switch t := jsonPath(doc, path).(type) {
	case bool:
		return t
	case string:
		v, err := strconv.ParseBool(t)
		return err == nil && v
	default:
		return false
	}
}

// mapFromPath returns the JSON object at path as a map, or nil when the path is
// absent or not an object. Used to carry a core's free-form per-turn metrics
// through the seam without the manifest declaring every metric field.
func mapFromPath(doc any, path string) map[string]any {
	if path == "" {
		return nil
	}
	m, ok := jsonPath(doc, path).(map[string]any)
	if !ok {
		return nil
	}
	return m
}

func stringSliceFromPath(doc any, path string) []string {
	arr, ok := jsonPath(doc, path).([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringSliceMapFromPath(doc any, path string) map[string][]string {
	m, ok := jsonPath(doc, path).(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string][]string, len(m))
	for key, raw := range m {
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		vals := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				vals = append(vals, s)
			}
		}
		out[key] = vals
	}
	return out
}
