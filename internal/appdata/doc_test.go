package appdata

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The filter takes only the whitelisted operators, on top-level and dotted
// fields; anything else is refused, never ignored.
func TestFilterWhitelist(t *testing.T) {
	for _, raw := range []string{
		`{"$or":[{"a":1},{"b":2}]}`,
		`{"$and":[{"a":1}]}`,
		`{"$where":"this.a == 1"}`,
		`{"a":{"$regex":"^x"}}`,
		`{"a":{"$elemMatch":{"b":1}}}`,
		`{"a":{"$not":{"$eq":1}}}`,
		`{"a":{"$eq":1,"b":2}}`,
		`{"a":{"$in":1}}`,
		`{"a":{"$exists":"yes"}}`,
		`{"a":{"$gt":[1]}}`,
		`{"a..b":1}`,
		`[1]`,
		`not json`,
	} {
		if _, err := ParseFilter(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseFilter(%s) = %v, want ErrInvalid", raw, err)
		}
	}
	doc := Render(Body{
		"choice": "lake", "count": json.Number("3"), "score": json.Number("2.5"),
		"who": map[string]any{"team": "red", "age": json.Number("30")}, "tags": []any{"a", "b"},
	}, Stamps{ID: "d1", By: "p1", CreatedAt: 100, UpdatedAt: 200, Version: 2})
	for _, c := range []struct {
		filter string
		want   bool
	}{
		{``, true},
		{`{"choice":"lake"}`, true},
		{`{"choice":"sea"}`, false},
		{`{"choice":{"$eq":"lake"}}`, true},
		{`{"choice":{"$ne":"lake"}}`, false},
		{`{"missing":{"$ne":"x"}}`, true},
		{`{"choice":{"$in":["sea","lake"]}}`, true},
		{`{"choice":{"$in":["sea"]}}`, false},
		{`{"count":{"$gt":2}}`, true},
		{`{"count":{"$gte":3,"$lt":4}}`, true},
		{`{"count":{"$lte":2}}`, false},
		{`{"count":3.0}`, true},
		{`{"score":{"$lt":3}}`, true},
		{`{"who.team":"red"}`, true},
		{`{"who.age":{"$gte":30}}`, true},
		{`{"who.team":{"$exists":true}}`, true},
		{`{"who.nope":{"$exists":false}}`, true},
		{`{"choice.deeper":{"$exists":true}}`, false},
		{`{"tags":["a","b"]}`, true},
		{`{"who":{"team":"red","age":30}}`, true},
		{`{"choice":{"$gt":"k"}}`, true},
		{`{"choice":{"$gt":1}}`, false},
		{`{"_by":"p1","_version":{"$gte":2}}`, true},
		{`{"_createdAt":{"$lt":150},"_id":"d1"}`, true},
		{`{"choice":"lake","count":{"$gt":5}}`, false},
	} {
		f, err := ParseFilter(c.filter)
		if err != nil {
			t.Fatalf("ParseFilter(%s): %v", c.filter, err)
		}
		if got := f.Match(doc); got != c.want {
			t.Errorf("filter %s matched %v, want %v", c.filter, got, c.want)
		}
	}
	f, _ := ParseFilter(`{"_by":{"$eq":"p9"}}`)
	if by, ok := f.By(); !ok || by != "p9" {
		t.Fatalf("By = %q %v", by, ok)
	}
	if _, ok := (Filter{}).By(); ok {
		t.Fatal("an empty filter names no author")
	}
}

// A client never sets a stamp: stamps it sends are dropped, other reserved
// names refused.
func TestParseBodyStampsAndReservedNames(t *testing.T) {
	b, err := ParseBody(json.RawMessage(`{"_id":"x","_by":"forged","_version":9,"_createdAt":1,"_updatedAt":1,"_author":{"kind":"member"},"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || b["a"] != json.Number("1") {
		t.Fatalf("ParseBody kept a stamp: %v", b)
	}
	for _, raw := range []string{`{"_secret":1}`, `{"$set":{"a":1}}`, `{"a":{"$gt":1}}`, `[1]`, `"x"`, `null`, `{"a":1} {"b":2}`} {
		if _, err := ParseBody(json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseBody(%s) = %v, want ErrInvalid", raw, err)
		}
	}
	big := Body{"text": strings.Repeat("x", wire.MaxAppDocBytes)}
	if _, err := big.Encode(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Encode of an oversized body = %v, want ErrTooLarge", err)
	}
	for _, id := range []string{"me", "venue-1", "a.b:c_d", NewDocID()} {
		if !ValidDocID(id) {
			t.Errorf("ValidDocID(%q) = false", id)
		}
	}
	for _, id := range []string{"", "a|b", "a/b", "a b", strings.Repeat("x", wire.MaxAppDocIDLen+1)} {
		if ValidDocID(id) {
			t.Errorf("ValidDocID(%q) = true", id)
		}
	}
	for _, name := range []string{"", "a.b", "a|b", strings.Repeat("x", wire.MaxAppCollectionNameLen+1)} {
		if ValidCollection(name) {
			t.Errorf("ValidCollection(%q) = true", name)
		}
	}
}

func TestPatchMergeAndInc(t *testing.T) {
	b := Body{"title": "x", "n": json.Number("1"), "f": json.Number("0.5"), "nested": map[string]any{"k": "v", "c": json.Number("2")}}
	out, err := b.Patch(json.RawMessage(`{"title":"y","nested":{"k":null,"new":true},"_by":"forged"}`),
		json.RawMessage(`{"n":2,"f":0.25,"nested.c":-2,"fresh.deep":5}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(out)
	want := `{"f":0.75,"fresh":{"deep":5},"n":3,"nested":{"c":0,"new":true},"title":"y"}`
	if string(got) != want {
		t.Fatalf("patched = %s, want %s", got, want)
	}
	if b["title"] != "x" {
		t.Fatal("Patch changed its receiver")
	}
	for _, c := range []struct{ merge, inc string }{
		{``, ``},
		{``, `{"title":1}`},
		{``, `{"n":"1"}`},
		{``, `{"title.x":1}`},
		{``, `{"_version":1}`},
		{``, `{"n":9223372036854775807}`},
		{`{"_secret":1}`, ``},
		{`[1]`, ``},
	} {
		if _, err := b.Patch(json.RawMessage(c.merge), json.RawMessage(c.inc)); !errors.Is(err, ErrInvalid) {
			t.Errorf("Patch(%s, %s) = %v, want ErrInvalid", c.merge, c.inc, err)
		}
	}
}
