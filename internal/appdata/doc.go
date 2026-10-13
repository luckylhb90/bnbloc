package appdata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A document is a JSON object the app writes (its Body) plus the stamps the
// server owns (§4.1): the app can read them, never set or change them.
const (
	StampID        = "_id"
	StampBy        = "_by"
	StampCreatedAt = "_createdAt"
	StampUpdatedAt = "_updatedAt"
	StampVersion   = "_version"
	// StampAuthor is the real identity behind _by, rendered only for the
	// app's owners.
	StampAuthor = "_author"
)

var stamps = map[string]bool{StampID: true, StampBy: true, StampCreatedAt: true, StampUpdatedAt: true, StampVersion: true, StampAuthor: true}

// Document errors: ErrInvalid is a 400 to the client, ErrTooLarge a 413.
var (
	ErrInvalid  = errors.New("invalid app document")
	ErrTooLarge = fmt.Errorf("app document over %d bytes", wire.MaxAppDocBytes)
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

var (
	collectionName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	docIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
)

// ValidCollection reports a collection name: letters, digits, '_' and '-',
// at most wire.MaxAppCollectionNameLen.
func ValidCollection(name string) bool {
	return len(name) <= wire.MaxAppCollectionNameLen && collectionName.MatchString(name)
}

// ValidDocID reports a document id: letters, digits, '_', '-', '.' and ':',
// at most wire.MaxAppDocIDLen (never "|", which the cipher's additional data
// separates with).
func ValidDocID(id string) bool {
	return len(id) <= wire.MaxAppDocIDLen && docIDPattern.MatchString(id)
}

// NewDocID mints a server document id: time-ordered, so a collection pages
// in insertion order.
func NewDocID() string { return ids.New("") }

// Body is a document's own fields: a JSON object, numbers kept exact
// (json.Number), with no stamp.
type Body map[string]any

func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, invalid("not JSON")
	}
	if dec.More() {
		return nil, invalid("trailing data after the JSON object")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, invalid("a document is a JSON object")
	}
	return obj, nil
}

// checkKeys refuses the keys a document may not hold: a top-level key
// starting with "_" (the server's) and any key starting with "$" (the
// operators').
func checkKeys(v any, top bool) error {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if strings.HasPrefix(k, "$") {
				return invalid("field %q: a name starting with $ is reserved", k)
			}
			if top && strings.HasPrefix(k, "_") {
				return invalid("field %q: a top-level name starting with _ is reserved", k)
			}
			if err := checkKeys(child, false); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := checkKeys(child, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropStamps removes the server's stamps from a client object: an app may
// send back a document it read, stamps included, and they are ignored.
func dropStamps(obj map[string]any) {
	for k := range obj {
		if stamps[k] {
			delete(obj, k)
		}
	}
}

// ParseBody reads a document an app sent: a JSON object whose stamps are
// ignored and whose other reserved names are refused.
func ParseBody(raw json.RawMessage) (Body, error) {
	obj, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	dropStamps(obj)
	if err := checkKeys(obj, true); err != nil {
		return nil, err
	}
	return Body(obj), nil
}

// Decode reads a stored body (an opened plaintext).
func Decode(raw []byte) (Body, error) {
	obj, err := decodeObject(raw)
	return Body(obj), err
}

// Encode is the body as stored: ErrTooLarge over wire.MaxAppDocBytes.
func (b Body) Encode() ([]byte, error) {
	raw, err := json.Marshal(map[string]any(b))
	if err != nil {
		return nil, err
	}
	if len(raw) > wire.MaxAppDocBytes {
		return nil, ErrTooLarge
	}
	return raw, nil
}

// Patch returns b with a PATCH's operations applied (wire.AppDocPatch; b
// itself is not changed): merge is a JSON merge patch (RFC 7396: an object
// merges, null removes a field; stamps in it are ignored) and inc adds
// numbers at top-level or dotted paths (a missing field counts as 0).
func (b Body) Patch(merge, inc json.RawMessage) (Body, error) {
	hasMerge := len(merge) > 0 && string(merge) != "null"
	hasInc := len(inc) > 0 && string(inc) != "null"
	if !hasMerge && !hasInc {
		return nil, invalid("a patch needs merge or $inc")
	}
	out := Body(deepCopy(map[string]any(b)).(map[string]any))
	if hasMerge {
		patch, err := decodeObject(merge)
		if err != nil {
			return nil, err
		}
		dropStamps(patch)
		if err := checkKeys(patch, true); err != nil {
			return nil, err
		}
		mergePatch(out, patch)
	}
	if hasInc {
		ops, err := decodeObject(inc)
		if err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(ops))
		for path := range ops {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			n, ok := ops[path].(json.Number)
			if !ok {
				return nil, invalid("$inc %q: the amount is a number", path)
			}
			if err := incAt(out, path, n); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func deepCopy(v any) any {
	switch v := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, c := range v {
			m[k] = deepCopy(c)
		}
		return m
	case []any:
		s := make([]any, len(v))
		for i, c := range v {
			s[i] = deepCopy(c)
		}
		return s
	}
	return v
}

// mergePatch applies an RFC 7396 merge patch to dst in place.
func mergePatch(dst, patch map[string]any) {
	for k, v := range patch {
		switch v := v.(type) {
		case nil:
			delete(dst, k)
		case map[string]any:
			sub, ok := dst[k].(map[string]any)
			if !ok {
				sub = map[string]any{}
			}
			mergePatch(sub, v)
			dst[k] = sub
		default:
			dst[k] = v
		}
	}
}

// splitPath splits a dotted field path; a top-level stamp or reserved name
// is refused (paths address the app's own fields).
func splitPath(path string) ([]string, error) {
	parts := strings.Split(path, ".")
	for _, p := range parts {
		if p == "" || strings.HasPrefix(p, "$") {
			return nil, invalid("field path %q", path)
		}
	}
	if strings.HasPrefix(parts[0], "_") {
		return nil, invalid("field path %q: a top-level name starting with _ is reserved", path)
	}
	return parts, nil
}

// incAt adds by to the number at path, creating the path's objects.
func incAt(b map[string]any, path string, by json.Number) error {
	parts, err := splitPath(path)
	if err != nil {
		return err
	}
	m := b
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p]
		if !ok {
			sub := map[string]any{}
			m[p], m = sub, sub
			continue
		}
		if m, ok = next.(map[string]any); !ok {
			return invalid("$inc %q: %q is not an object", path, p)
		}
	}
	last := parts[len(parts)-1]
	cur := json.Number("0")
	if v, ok := m[last]; ok {
		if cur, ok = v.(json.Number); !ok {
			return invalid("$inc %q: the field is not a number", path)
		}
	}
	sum, err := addNumbers(cur, by)
	if err != nil {
		return invalid("$inc %q: %v", path, err)
	}
	m[last] = sum
	return nil
}

// addNumbers adds two JSON numbers: exactly when both are integers, in
// float64 otherwise.
func addNumbers(a, b json.Number) (json.Number, error) {
	ai, aerr := strconv.ParseInt(string(a), 10, 64)
	bi, berr := strconv.ParseInt(string(b), 10, 64)
	if aerr == nil && berr == nil {
		sum := ai + bi
		if (bi > 0 && sum < ai) || (bi < 0 && sum > ai) {
			return "", errors.New("integer overflow")
		}
		return json.Number(strconv.FormatInt(sum, 10)), nil
	}
	af, err := a.Float64()
	if err != nil {
		return "", err
	}
	bf, err := b.Float64()
	if err != nil {
		return "", err
	}
	sum := af + bf
	if math.IsInf(sum, 0) || math.IsNaN(sum) {
		return "", errors.New("out of range")
	}
	return json.Number(strconv.FormatFloat(sum, 'g', -1, 64)), nil
}

// Stamps are a stored document's server fields.
type Stamps struct {
	ID        string
	By        string
	CreatedAt int64
	UpdatedAt int64
	Version   int64
	// Author is the participant key ("<kind>:<id>") behind By, set only
	// for an owner's read.
	Author string
}

// Render is the document an app reads: its body with the server's stamps.
func Render(b Body, s Stamps) map[string]any {
	out := make(map[string]any, len(b)+6)
	for k, v := range b {
		out[k] = v
	}
	out[StampID], out[StampBy] = s.ID, s.By
	out[StampCreatedAt], out[StampUpdatedAt], out[StampVersion] = s.CreatedAt, s.UpdatedAt, s.Version
	if s.Author != "" {
		kind, id, _ := strings.Cut(s.Author, ":")
		out[StampAuthor] = map[string]any{"kind": kind, "id": id}
	}
	return out
}
