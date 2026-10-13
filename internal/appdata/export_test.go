package appdata

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// CSVCell: text as is (formula-like text defused), numbers and booleans as
// written, null or absent empty, anything nested its JSON text.
func TestCSVCell(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{"plain", "plain"},
		{"", ""},
		{nil, ""},
		{json.Number("0"), "0"},
		{json.Number("-3"), "-3"},
		{int64(12), "12"},
		{false, "false"},
		{"=HYPERLINK(1)", "'=HYPERLINK(1)"},
		{"+1", "'+1"},
		{"-1", "'-1"},
		{"@x", "'@x"},
		{"\tx", "'\tx"},
		{"\rx", "'\rx"},
		{"a=b", "a=b"},
		{map[string]any{"b": []any{json.Number("1"), json.Number("2")}, "a": json.Number("1")}, `{"a":1,"b":[1,2]}`},
		{[]any{"x", "y"}, `["x","y"]`},
	} {
		if got := CSVCell(c.in); got != c.want {
			t.Errorf("CSVCell(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A collection's CSV: the stamps present first in their order, then every
// other field by first appearance (a document's own in name order); every
// row the same columns; quoting by the CSV rules; CRLF line ends.
func TestCSVWriter(t *testing.T) {
	docs := []map[string]any{
		{"choice": "B", StampID: "d1", StampBy: "p1", StampVersion: int64(1), StampCreatedAt: int64(10), StampUpdatedAt: int64(10), "meta": map[string]any{"device": "phone"}},
		{StampID: "d2", StampBy: "p2", StampVersion: int64(2), StampCreatedAt: int64(11), StampUpdatedAt: int64(12), "note": "hi, there", "choice": `say "A"`,
			StampAuthor: map[string]any{"kind": "visitor", "id": "vis_1"}},
	}
	var cols CSVColumns
	for _, d := range docs {
		cols.Add(d)
	}
	want := []string{"_id", "_by", "_author", "_createdAt", "_updatedAt", "_version", "choice", "meta", "note"}
	if !slices.Equal(cols.Names(), want) {
		t.Fatalf("columns = %v, want %v", cols.Names(), want)
	}
	var b strings.Builder
	w, err := NewCSVWriter(&b, cols.Names())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if err := w.Write(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	got := strings.Split(b.String(), "\r\n")
	wantRows := []string{
		"_id,_by,_author,_createdAt,_updatedAt,_version,choice,meta,note",
		`d1,p1,,10,10,1,B,"{""device"":""phone""}",`,
		`d2,p2,"{""id"":""vis_1"",""kind"":""visitor""}",11,12,2,"say ""A""",,"hi, there"`,
		"",
	}
	if !slices.Equal(got, wantRows) {
		t.Fatalf("csv =\n%q\nwant\n%q", got, wantRows)
	}

	// No documents: an empty header line.
	var empty strings.Builder
	w, _ = NewCSVWriter(&empty, (&CSVColumns{}).Names())
	if err := w.Flush(); err != nil || empty.String() != "\r\n" {
		t.Fatalf("empty csv = %q, %v", empty.String(), err)
	}
}
