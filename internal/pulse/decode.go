package pulse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Errors Decode returns. A stream entry that fails one of them is dropped and
// counted by the consumer; it is never guessed at.
var (
	ErrVersion     = errors.New("pulse: envelope version is not 1")
	ErrUnknownKind = errors.New("pulse: unknown kind")
)

// ErrRefused reports an event the schema refuses (the same rules Emit applies).
type ErrRefused struct{ Reason string }

func (e ErrRefused) Error() string { return "pulse: event refused: " + e.Reason }

// Decode reads one envelope from its JSON (MarshalJSON's shape) and rebuilds the
// event through the same schema check Emit uses. A consumer therefore accepts
// nothing the product could not have emitted.
func Decode(raw []byte) (Event, error) {
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Event{}, fmt.Errorf("pulse: decode: %w", err)
	}
	if w.V != Version {
		return Event{}, ErrVersion
	}
	spec, ok := kinds[w.Kind]
	if !ok {
		return Event{}, ErrUnknownKind
	}
	ev := Event{ID: w.ID, Tenant: w.Tenant, Kind: w.Kind, At: w.At}
	keys := make([]string, 0, len(w.Attrs))
	for k := range w.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys) // a stable order, so decoding is deterministic
	for _, k := range keys {
		want, known := spec.attrs[k]
		if !known {
			continue // the schema check would drop it too
		}
		a, ok := attrFrom(k, want.typ, w.Attrs[k])
		if !ok {
			continue
		}
		ev = ev.With(a)
	}
	out, _, r := check(ev)
	if r != reasonOK {
		return Event{}, ErrRefused{Reason: r.String()}
	}
	return out, nil
}

// attrFrom rebuilds one attribute from its JSON value, if the value has the
// shape the schema declares for it.
func attrFrom(key string, typ attrType, v any) (Attr, bool) {
	switch typ {
	case typeNum:
		if f, ok := v.(float64); ok {
			return Num(key, f), true
		}
	case typeFlag:
		if b, ok := v.(bool); ok {
			return Flag(key, b), true
		}
	default:
		if s, ok := v.(string); ok {
			return Attr{key: key, typ: typ, s: s}, true
		}
	}
	return Attr{}, false
}

// Digest is the hex SHA-256 of s: an identifier that is stable and
// one-way. The product uses it where an event needs to name a row (a job)
// without carrying the row's id.
func Digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (r reason) String() string {
	switch r {
	case reasonTenant:
		return "tenant"
	case reasonID:
		return "id"
	case reasonKind:
		return "kind"
	case reasonAttrCount:
		return "attribute count"
	case reasonRequired:
		return "required attribute missing"
	}
	return "ok"
}
