// Package sse reads a text/event-stream: the room event stream realtime
// serves (GET /v1/rooms/{id}/events). It is the one parser every client in
// this repo uses — the bridge, the `bnbloc smoke` check and the load tool —
// so they agree on comments (the keep-alive), multi-line data and dispatch.
// Reconnecting is the caller's business.
package sse

import (
	"bufio"
	"bytes"
	"io"
)

// MaxLineBytes bounds one line of the stream. A realtime event is one data
// line: its payload is bounded by the 1 MiB internal publish body, plus the
// event envelope. The bound leaves headroom over that and still caps the
// memory a misbehaving server can make a client buffer; a longer line fails
// Next with bufio.ErrTooLong.
const MaxLineBytes = 4 << 20

// Event is one dispatched event.
type Event struct {
	// ID is the event's "id:" field ("" when it has none; realtime sends one
	// for durable events only, the room sequence).
	ID string
	// Name is the event's "event:" field ("" when it has none).
	Name string
	// Data is the event's "data:" lines joined by "\n".
	Data []byte
}

// Reader reads events from a stream.
type Reader struct {
	sc *bufio.Scanner
	// OnLine, when set, runs for every line read — fields, blank lines and
	// comments alike — so a caller can watch the stream's liveness (the
	// server's keep-alive is a comment line).
	OnLine func()
}

// NewReader reads events from r.
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), MaxLineBytes)
	return &Reader{sc: sc}
}

// Next returns the next event that carries data, following the
// text/event-stream rules: a line is "field: value" (one space after the
// colon is dropped; a line without a colon is a field with an empty value),
// a line starting with ":" is a comment, a blank line dispatches the event,
// and an event whose data is empty is dropped. Lines end in "\n" or "\r\n".
// At the end of the stream Next returns io.EOF and discards an event not
// terminated by a blank line; any other read error is returned as is.
func (r *Reader) Next() (Event, error) {
	var ev Event
	var data []byte
	hasData := false
	for r.sc.Scan() {
		if r.OnLine != nil {
			r.OnLine()
		}
		line := r.sc.Bytes()
		if len(line) == 0 {
			if len(data) > 0 {
				ev.Data = data
				return ev, nil
			}
			ev, hasData = Event{}, false
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "data":
			if hasData {
				data = append(data, '\n')
			}
			data = append(data, value...)
			hasData = true
		case "event":
			ev.Name = string(value)
		case "id":
			ev.ID = string(value)
		}
	}
	if err := r.sc.Err(); err != nil {
		return Event{}, err
	}
	return Event{}, io.EOF
}
