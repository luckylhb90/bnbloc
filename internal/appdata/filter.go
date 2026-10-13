package appdata

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Filter is a find's `filter` (§4.1): field conditions combined with AND,
// over top-level and dotted fields of the document as the app reads it
// (stamps included). A field's condition is a value (equality) or an object
// of operators drawn only from the whitelist; any other operator is refused.
// Bodies are sealed in the database, so a filter runs on the decrypted page
// in the service (only an equality on _by also narrows the query).
type Filter struct {
	conds []fieldCond
}

type fieldCond struct {
	field string
	path  []string
	ops   []filterOp
}

type filterOp struct {
	name string
	arg  any
}

// FilterOps is the operator whitelist.
var FilterOps = []string{"$eq", "$ne", "$in", "$gt", "$gte", "$lt", "$lte", "$exists"}

func knownOp(name string) bool {
	for _, op := range FilterOps {
		if op == name {
			return true
		}
	}
	return false
}

// ParseFilter reads a filter ("" = match everything). An unknown operator,
// a top-level operator ($and, $or, …) or a malformed argument is ErrInvalid.
func ParseFilter(raw string) (Filter, error) {
	if strings.TrimSpace(raw) == "" {
		return Filter{}, nil
	}
	obj, err := decodeObject([]byte(raw))
	if err != nil {
		return Filter{}, invalid("filter is not a JSON object")
	}
	fields := make([]string, 0, len(obj))
	for k := range obj {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	var f Filter
	for _, field := range fields {
		if strings.HasPrefix(field, "$") {
			return Filter{}, invalid("unknown filter operator %s", field)
		}
		path := strings.Split(field, ".")
		for _, p := range path {
			if p == "" {
				return Filter{}, invalid("filter field %q", field)
			}
		}
		c := fieldCond{field: field, path: path}
		v := obj[field]
		if m, ok := v.(map[string]any); ok && hasOperator(m) {
			names := make([]string, 0, len(m))
			for name := range m {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if !knownOp(name) {
					if strings.HasPrefix(name, "$") {
						return Filter{}, invalid("unknown filter operator %s", name)
					}
					return Filter{}, invalid("filter field %q mixes operators and fields", field)
				}
				if err := checkOpArg(name, m[name]); err != nil {
					return Filter{}, err
				}
				c.ops = append(c.ops, filterOp{name: name, arg: m[name]})
			}
		} else {
			c.ops = []filterOp{{name: "$eq", arg: v}}
		}
		f.conds = append(f.conds, c)
	}
	return f, nil
}

func hasOperator(m map[string]any) bool {
	for k := range m {
		if strings.HasPrefix(k, "$") {
			return true
		}
	}
	return false
}

func checkOpArg(name string, arg any) error {
	switch name {
	case "$in":
		if _, ok := arg.([]any); !ok {
			return invalid("%s takes an array", name)
		}
	case "$exists":
		if _, ok := arg.(bool); !ok {
			return invalid("%s takes true or false", name)
		}
	case "$gt", "$gte", "$lt", "$lte":
		switch arg.(type) {
		case json.Number, string:
		default:
			return invalid("%s takes a number or a string", name)
		}
	}
	return nil
}

// Empty reports a filter that matches every document.
func (f Filter) Empty() bool { return len(f.conds) == 0 }

// By returns the pseudonym the filter requires _by to equal, so the store
// can narrow by it.
func (f Filter) By() (string, bool) {
	for _, c := range f.conds {
		if c.field != StampBy {
			continue
		}
		for _, op := range c.ops {
			if s, ok := op.arg.(string); ok && op.name == "$eq" {
				return s, true
			}
		}
	}
	return "", false
}

// Match reports whether doc (a rendered document) meets every condition.
func (f Filter) Match(doc map[string]any) bool {
	for _, c := range f.conds {
		v, present := lookup(doc, c.path)
		for _, op := range c.ops {
			if !apply(op, v, present) {
				return false
			}
		}
	}
	return true
}

func lookup(doc map[string]any, path []string) (any, bool) {
	var cur any = doc
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func apply(op filterOp, v any, present bool) bool {
	switch op.name {
	case "$exists":
		return present == op.arg.(bool)
	case "$eq":
		return present && equal(v, op.arg)
	case "$ne":
		return !present || !equal(v, op.arg)
	case "$in":
		if !present {
			return false
		}
		for _, x := range op.arg.([]any) {
			if equal(v, x) {
				return true
			}
		}
		return false
	}
	if !present {
		return false
	}
	c, ok := compare(v, op.arg)
	if !ok {
		return false
	}
	switch op.name {
	case "$gt":
		return c > 0
	case "$gte":
		return c >= 0
	case "$lt":
		return c < 0
	case "$lte":
		return c <= 0
	}
	return false
}

// number reads a JSON number of a body (json.Number) or a stamp (int64).
func number(v any) (float64, bool) {
	switch v := v.(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}

// compare orders two numbers or two strings; other pairs do not compare.
func compare(a, b any) (int, bool) {
	if x, ok := number(a); ok {
		y, ok := number(b)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	}
	x, ok := a.(string)
	if !ok {
		return 0, false
	}
	y, ok := b.(string)
	if !ok {
		return 0, false
	}
	return strings.Compare(x, y), true
}

// equal is JSON equality: numbers by value, objects and arrays deeply.
func equal(a, b any) bool {
	if x, ok := number(a); ok {
		y, ok := number(b)
		if !ok {
			return false
		}
		if ai, aok := intOf(a); aok {
			if bi, bok := intOf(b); bok {
				return ai == bi
			}
		}
		return x == y
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool, string:
		return a == b
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !equal(xv, yv) {
				return false
			}
		}
		return true
	}
	return false
}

func intOf(v any) (int64, bool) {
	switch v := v.(type) {
	case json.Number:
		n, err := strconv.ParseInt(string(v), 10, 64)
		return n, err == nil
	case int64:
		return v, true
	case int:
		return int64(v), true
	}
	return 0, false
}
