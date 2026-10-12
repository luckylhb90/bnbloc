// Package sample exercises every form wiregen renders.
package sample

import "encoding/json"

// Kind is a named string type: the union of its constants.
type Kind string

// Kinds.
const (
	KindA Kind = "a"
	// KindB is documented.
	KindB Kind = "b"
)

// Limits are plain constants.
const (
	MaxBytes = 8 << 20
	Ratio    = 0.5
	Enabled  = true
)

// Event kinds collected into one union across blocks.
//
//wire:union EventKind
const (
	EventOne = "one"
	EventTwo = "two"
)

//wire:union EventKind
const EventThree = "three"

// Base is embedded.
type Base struct {
	ID string `json:"id"`
}

// Item shows field forms.
type Item struct {
	Base
	// Name is required.
	Name     string          `json:"name"`
	Note     string          `json:"note,omitempty"` // trailing comment
	Kind     Kind            `json:"kind"`
	Tags     []string        `json:"tags"`
	Counts   map[string]int  `json:"counts,omitempty"`
	ByKind   map[Kind]int    `json:"byKind,omitempty"`
	Parent   *Base           `json:"parent"`
	Child    *Base           `json:"child,omitempty"`
	Raw      json.RawMessage `json:"raw"`
	Any      any             `json:"any"`
	Bytes    []byte          `json:"bytes"`
	Role     string          `json:"role" tstype:"Kind"`
	Roles    []string        `json:"roles" tstype:"Kind"`
	Event    string          `json:"event" tstype:"EventKind"`
	Big      int64           `json:"big,string"`
	Inline   struct{ X int } `json:"inline"`
	Untagged int
	Hidden   string `json:"-"`
	private  string
}

// Kinds lists every kind in display order.
var Kinds = []Kind{KindA, KindB}

// Spec is a registry row.
type Spec struct {
	Kind    string `json:"kind"`
	Durable bool   `json:"durable"`
}

// Specs is a registry of constant rows; its Kind fields are a union.
//
//wire:union SpecKind Kind
var Specs = []Spec{{Kind: EventOne, Durable: true}, {Kind: EventTwo}}

// ByName is a constant map.
var ByName = map[string]int{"one": 1}

// Computed is skipped: its value is not a constant.
var Computed = len(Kinds)

// Alias is a slice type.
type Alias []Item

func (i Item) unused() string { return i.private }
