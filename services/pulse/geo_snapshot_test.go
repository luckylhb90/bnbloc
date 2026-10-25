package pulsesvc

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/pulse"
)

// The snapshot lists a country only with at least k people online, and says how many
// were withheld. Members are placed through the same events the stream sends.
func TestSnapshotListsOnlyCountriesAtKAnonymity(t *testing.T) {
	rdb := testRedis(t)
	tnt := uniq("tenant")
	ctx := context.Background()
	now := time.Now()
	state := NewLive(rdb)
	place := func(n int, country string) {
		pipe := rdb.Pipeline()
		for i := 0; i < n; i++ {
			m := pulse.Digest(fmt.Sprintf("%s-%s-%d", tnt, country, i))
			ev := pulse.Event{ID: "g" + m, Tenant: tnt, Kind: pulse.KindPresenceSeen, At: now.UnixMilli()}.
				With(pulse.Hash("member", m)).With(pulse.Ident("country", country))
			state.Apply(pipe, ctx, ev)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	place(5, "de")
	place(2, "fr")
	srv := &Server{State: state, Rows: newFakeRows(), Now: func() time.Time { return now }}
	rec, snap := snapshotFor(t, srv, tnt)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(snap.Geo.Countries) != 1 || snap.Geo.Countries[0].Code != "DE" || snap.Geo.Countries[0].People != 5 {
		t.Fatalf("countries = %+v; want DE with 5 only", snap.Geo.Countries)
	}
	if snap.Geo.Countries[0].Lat == 0 && snap.Geo.Countries[0].Lon == 0 {
		t.Fatal("a listed country must carry its placement")
	}
	if snap.Geo.CountriesWithheld != 1 {
		t.Fatalf("withheld = %d, want 1 (FR, two people)", snap.Geo.CountriesWithheld)
	}
}

// With no country yet the list is an empty array, never null: the board maps over it.
func TestSnapshotCountriesIsAnEmptyArrayWhenNobodyIsPlaced(t *testing.T) {
	srv := &Server{State: NewLive(testRedis(t)), Rows: newFakeRows(), Now: time.Now}
	rec, _ := snapshotFor(t, srv, uniq("tenant"))
	if !strings.Contains(rec.Body.String(), `"countries":[]`) {
		t.Fatalf("countries must encode as [], got %s", rec.Body.String())
	}
}
