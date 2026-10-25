package pulsesvc

import "testing"

func TestRecentIDsDropsRedeliveriesWithinItsWindow(t *testing.T) {
	r := newRecentIDs(3)
	if r.Seen("a") || r.Seen("b") || r.Seen("c") {
		t.Fatal("first sightings must not be duplicates")
	}
	if !r.Seen("b") {
		t.Fatal("a redelivery within the window must be recognised")
	}
	r.Seen("d") // evicts "a", the oldest
	if r.Seen("a") {
		t.Fatal("an id that aged out of the window is counted again: the bound is the documented trade-off")
	}
}
