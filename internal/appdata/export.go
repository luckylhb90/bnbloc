package appdata

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
)

// An owner's export of an app's data (wire.AppExportJSON/CSV): the one
// owner of the CSV form, which the room service streams (services/room
// appdata_export.go). A CSV is one collection: the stamps present first
// (ExportStamps' order), then every other top-level field in order of first
// appearance (a document's own fields in name order, as the JSON form
// writes them), one row per document, CRLF line ends.

// ExportStamps are the stamps of a rendered document, the CSV's first
// columns in this order (_author: an owner's read, which an export is).
var ExportStamps = []string{StampID, StampBy, StampAuthor, StampCreatedAt, StampUpdatedAt, StampVersion}

// CSVColumns collects a collection's columns from its rendered documents.
type CSVColumns struct {
	seen   map[string]bool
	fields []string
}

// Add takes one document's fields.
func (c *CSVColumns) Add(doc map[string]any) {
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !c.seen[k] {
			c.seen[k] = true
			if !stamps[k] {
				c.fields = append(c.fields, k)
			}
		}
	}
}

// Names are the columns: the stamps seen, then the fields.
func (c *CSVColumns) Names() []string {
	var out []string
	for _, s := range ExportStamps {
		if c.seen[s] {
			out = append(out, s)
		}
	}
	return append(out, c.fields...)
}

// CSVWriter writes a collection's rows under its columns.
type CSVWriter struct {
	w       *csv.Writer
	columns []string
	row     []string
}

// NewCSVWriter writes the header row of columns to w.
func NewCSVWriter(w io.Writer, columns []string) (*CSVWriter, error) {
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	header := make([]string, len(columns))
	for i, c := range columns {
		header[i] = CSVCell(c)
	}
	if err := cw.Write(header); err != nil {
		return nil, err
	}
	return &CSVWriter{w: cw, columns: columns, row: make([]string, len(columns))}, nil
}

// Write writes one document's row (fields outside the columns are left out).
func (c *CSVWriter) Write(doc map[string]any) error {
	for i, col := range c.columns {
		c.row[i] = CSVCell(doc[col])
	}
	return c.w.Write(c.row)
}

// Flush writes what is buffered and reports a write failure.
func (c *CSVWriter) Flush() error {
	c.w.Flush()
	return c.w.Error()
}

// CSVCell is one value as the text of a CSV cell (the writer quotes a cell
// holding a comma, quote or line break, quotes doubled): text as is,
// except text a spreadsheet would run as a formula (= + - @, a tab or a
// carriage return first: the data is anyone's with the link), which gets a
// leading apostrophe; numbers and booleans as written; null or absent
// empty; anything nested (an object or an array, at any depth) its JSON
// text in the one cell, so every row has the same columns.
func CSVCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		if x != "" && strings.ContainsRune("=+-@\t\r", rune(x[0])) {
			return "'" + x
		}
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
