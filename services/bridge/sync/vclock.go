package sync

// VectorClock tracks per-peer version counters for one file path. It captures
// causality so the engine can tell "strictly newer" from "true concurrent
// edit" — strictly better than last-writer-wins, which loses data on clock
// skew or equal mtimes.
type VectorClock map[string]int64

// Clone returns an independent copy.
func (v VectorClock) Clone() VectorClock {
	out := make(VectorClock, len(v))
	for k, n := range v {
		out[k] = n
	}
	return out
}

// Bump increments the counter for peer and returns the clock (for chaining).
func (v VectorClock) Bump(peer string) VectorClock {
	v[peer]++
	return v
}

// Ordering describes the causal relation between two clocks.
type Ordering int

const (
	// Equal: identical causal history.
	Equal Ordering = iota
	// Dominates: the left clock is strictly newer (a superset of history).
	Dominates
	// Dominated: the left clock is strictly older.
	Dominated
	// Concurrent: neither dominates — a genuine conflict.
	Concurrent
)

// Compare returns how a relates to b. a Dominates b means a is newer.
func (a VectorClock) Compare(b VectorClock) Ordering {
	aGreater, bGreater := false, false
	// Every peer present in either clock; absent entries are 0.
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	for k := range seen {
		av, bv := a[k], b[k]
		if av > bv {
			aGreater = true
		} else if av < bv {
			bGreater = true
		}
	}
	switch {
	case aGreater && bGreater:
		return Concurrent
	case aGreater:
		return Dominates
	case bGreater:
		return Dominated
	default:
		return Equal
	}
}

// Merge returns the element-wise maximum of a and b: the joined causal history
// used after a successful three-way merge of concurrent edits.
func (a VectorClock) Merge(b VectorClock) VectorClock {
	out := a.Clone()
	for k, bv := range b {
		if bv > out[k] {
			out[k] = bv
		}
	}
	return out
}
