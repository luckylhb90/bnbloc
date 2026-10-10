package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func readAll(t *testing.T, body string) ([]Event, int, error) {
	t.Helper()
	r := NewReader(strings.NewReader(body))
	lines := 0
	r.OnLine = func() { lines++ }
	var out []Event
	for {
		ev, err := r.Next()
		if err != nil {
			return out, lines, err
		}
		out = append(out, ev)
	}
}

// The stream realtime writes: a connect comment, keep-alive comments, named
// events with an id (durable) or without (transient), CRLF line ends, and
// multi-line data.
func TestReaderParsesTheRoomStream(t *testing.T) {
	body := ": connected\n\n" +
		"id: 7\nevent: chat_message\ndata: {\"sequence\":7}\n\n" +
		": keep-alive\n\n" +
		"event: presence_join\r\ndata: {\"a\":1,\r\ndata: \"b\":2}\r\n\r\n" +
		"data:no-space\n\n"
	got, lines, err := readAll(t, body)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF at the end of the stream", err)
	}
	want := []Event{
		{ID: "7", Name: "chat_message", Data: []byte(`{"sequence":7}`)},
		{Name: "presence_join", Data: []byte("{\"a\":1,\n\"b\":2}")},
		{Data: []byte("no-space")},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %d", got, len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Name != want[i].Name || string(got[i].Data) != string(want[i].Data) {
			t.Fatalf("event %d = {%q %q %q}, want {%q %q %q}", i, got[i].ID, got[i].Name, got[i].Data, want[i].ID, want[i].Name, want[i].Data)
		}
	}
	if lines != 14 {
		t.Fatalf("OnLine ran %d times, want 14 (every line, comments and blank lines included)", lines)
	}
}

// Fields never leak from one event into the next: a dispatched event, or a
// blank line ending an event without data, resets the id and the name.
func TestReaderResetsFieldsBetweenEvents(t *testing.T) {
	body := "id: 1\nevent: a\ndata: x\n\n" +
		"event: dropped\nid: 9\n\n" + // no data: dropped, and its fields reset
		"data: y\n\n" +
		"event: empty\ndata:\n\n" // empty data: dropped
	got, _, err := readAll(t, body)
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].ID != "" || got[1].Name != "" || string(got[1].Data) != "y" {
		t.Fatalf("events = %+v, want x then an unnamed y with no id", got)
	}
}

// An event cut off by the end of the stream (no blank line) is discarded,
// as is unknown field noise.
func TestReaderDiscardsAnUnterminatedEvent(t *testing.T) {
	got, _, err := readAll(t, "retry: 10\nfoo\ndata: done\n\nevent: x\ndata: partial")
	if !errors.Is(err, io.EOF) || len(got) != 1 || string(got[0].Data) != "done" {
		t.Fatalf("events = %+v, err = %v; want only the terminated event, then io.EOF", got, err)
	}
}

// A line longer than MaxLineBytes fails the read instead of buffering
// without bound; a read error surfaces unchanged.
func TestReaderBoundsLinesAndSurfacesErrors(t *testing.T) {
	long := "data: " + strings.Repeat("x", MaxLineBytes) + "\n\n"
	if _, _, err := readAll(t, long); !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("err = %v, want bufio.ErrTooLong", err)
	}
	boom := errors.New("connection reset")
	r := NewReader(io.MultiReader(strings.NewReader("data: a\n\ndata: b"), errReader{boom}))
	if ev, err := r.Next(); err != nil || string(ev.Data) != "a" {
		t.Fatalf("first = %q, %v", ev.Data, err)
	}
	if _, err := r.Next(); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the read error", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
