package pulse

import "encoding/json"

// wire is the JSON shape of an event (envelope v1, docs/prd/2026-10-24-executive-pulse-backend.md, section 3).
type wire struct {
	V      int            `json:"v"`
	ID     string         `json:"id"`
	Tenant string         `json:"tenant"`
	Kind   string         `json:"kind"`
	At     int64          `json:"at"`
	Attrs  map[string]any `json:"attrs"`
}

// MarshalJSON encodes the envelope. Attributes encode as their value: numbers,
// booleans or strings. It runs on the batcher, never on the product's path.
func (e Event) MarshalJSON() ([]byte, error) {
	attrs := make(map[string]any, e.n)
	for _, a := range e.attrs[:e.n] {
		attrs[a.key] = a.value()
	}
	return json.Marshal(wire{V: Version, ID: e.ID, Tenant: e.Tenant, Kind: e.Kind, At: e.At, Attrs: attrs})
}

func (a Attr) value() any {
	switch a.typ {
	case typeNum:
		return a.n
	case typeFlag:
		return a.b
	default:
		return a.s
	}
}
