// Package pulse is the product's side of the organization board. It turns facts
// the product already stores into closed, bounded events and hands them to a
// Sink without ever blocking the caller. The contract and the non-impact rules
// are in docs/prd/2026-10-24-executive-pulse-backend.md.
//
// The envelope carries no free text, no names and no content: an attribute is
// one of six typed shapes (Enum, Num, Flag, Hash, Zone, Ident), and each kind
// accepts only the attributes its schema names (schema.go).
package pulse

import "time"

// Version is the envelope version the consumer reads.
const Version = 1

// Bounds on what an event may carry. They keep the queue's memory fixed.
const (
	maxAttrs     = 6
	maxTenantLen = 64
	maxIDLen     = 128
)

type attrType uint8

const (
	typeEnum attrType = iota + 1
	typeNum
	typeFlag
	typeHash
	typeZone
	typeIdent
)

// Attr is one attribute of an event. Build it only with the constructors below.
type Attr struct {
	key string
	typ attrType
	s   string
	n   float64
	b   bool
}

// Enum is a value from a closed set the schema names for key.
func Enum(key, value string) Attr { return Attr{key: key, typ: typeEnum, s: value} }

// Num is a non-negative finite number (a count, a duration, a token count).
func Num(key string, value float64) Attr { return Attr{key: key, typ: typeNum, n: value} }

// Flag is a yes or no fact.
func Flag(key string, value bool) Attr { return Attr{key: key, typ: typeFlag, b: value} }

// Hash is a 64-character lowercase hex digest, from MemberHash. It is the only
// form in which a person may appear in an event.
func Hash(key, value string) Attr { return Attr{key: key, typ: typeHash, s: value} }

// Zone is an IANA time-zone name ("Europe/Berlin") or "other".
func Zone(key, value string) Attr { return Attr{key: key, typ: typeZone, s: value} }

// Ident is a short lowercase identifier (letters, digits, '_', '-', '.'), for
// values a schema cannot enumerate, such as an artifact kind.
func Ident(key, value string) Attr { return Attr{key: key, typ: typeIdent, s: value} }

// Event is one fact for the board. It is a value type with a fixed attribute
// array, so building and queueing it allocates nothing.
type Event struct {
	ID     string // unique per fact; the consumer de-duplicates on it
	Tenant string
	Kind   string // one of the Kind* constants
	At     int64  // unix milliseconds; zero stamps the emit time
	attrs  [maxAttrs]Attr
	n      int
	over   bool
}

// With returns the event with one more attribute. More than maxAttrs marks the
// event invalid (a programming error the emitter counts).
func (e Event) With(a Attr) Event {
	if e.n < maxAttrs {
		e.attrs[e.n] = a
		e.n++
	} else {
		e.over = true
	}
	return e
}

// Attrs returns the attributes in the order they were added.
func (e Event) Attrs() []Attr { return e.attrs[:e.n] }

// Reason codes why an event was not accepted. They index the emitter's counters.
type reason int

const (
	reasonOK reason = iota
	reasonTenant
	reasonID
	reasonKind
	reasonAttrCount
	reasonRequired
	numReasons
)

// check validates the event against its schema. It returns the event with the
// unknown or ill-typed attributes removed (counted in dropped), or the reason it
// must be refused.
func check(e Event) (Event, int, reason) {
	if e.Tenant == "" || len(e.Tenant) > maxTenantLen {
		return e, 0, reasonTenant
	}
	if e.ID == "" || len(e.ID) > maxIDLen {
		return e, 0, reasonID
	}
	spec, ok := kinds[e.Kind]
	if !ok {
		return e, 0, reasonKind
	}
	if e.over {
		return e, 0, reasonAttrCount
	}
	if e.At == 0 {
		e.At = time.Now().UnixMilli()
	}
	out := Event{ID: e.ID, Tenant: e.Tenant, Kind: e.Kind, At: e.At}
	dropped := 0
	for _, a := range e.attrs[:e.n] {
		want, known := spec.attrs[a.key]
		if !known || !want.accepts(a) {
			dropped++
			continue
		}
		out = out.With(a)
	}
	for _, key := range spec.required {
		if !out.has(key) {
			return e, dropped, reasonRequired
		}
	}
	return out, dropped, reasonOK
}

func (e Event) has(key string) bool {
	for _, a := range e.attrs[:e.n] {
		if a.key == key {
			return true
		}
	}
	return false
}

// Key is the attribute's name.
func (a Attr) Key() string { return a.key }

// Text is an enum, zone, hash or identifier value ("" for other shapes).
func (a Attr) Text() string { return a.s }

// Number is a numeric value (0 for other shapes).
func (a Attr) Number() float64 { return a.n }

// Bool is a flag value (false for other shapes).
func (a Attr) Bool() bool { return a.b }

// Get returns the attribute named key, if the event has it.
func (e Event) Get(key string) (Attr, bool) {
	for _, a := range e.attrs[:e.n] {
		if a.key == key {
			return a, true
		}
	}
	return Attr{}, false
}
