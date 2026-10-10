package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func msDurations(vals ...int) []time.Duration {
	out := make([]time.Duration, len(vals))
	for i, v := range vals {
		out[i] = time.Duration(v) * time.Millisecond
	}
	return out
}

func TestSummarizeNearestRank(t *testing.T) {
	// 1..100 ms, shuffled order: nearest rank gives the exact sample.
	var vals []int
	for i := 100; i >= 1; i-- {
		vals = append(vals, i)
	}
	got := summarize(msDurations(vals...))
	want := Percentiles{Count: 100, Mean: 50.5, P50: 50, P95: 95, P99: 99, Max: 100}
	if got != want {
		t.Fatalf("summarize(1..100) = %+v, want %+v", got, want)
	}
}

func TestSummarizeSmallSamples(t *testing.T) {
	cases := []struct {
		name string
		in   []int
		want Percentiles
	}{
		{"empty", nil, Percentiles{}},
		{"one", []int{7}, Percentiles{Count: 1, Mean: 7, P50: 7, P95: 7, P99: 7, Max: 7}},
		// n=3: p50 rank ceil(1.5)=2, p95 rank ceil(2.85)=3.
		{"three", []int{30, 10, 20}, Percentiles{Count: 3, Mean: 20, P50: 20, P95: 30, P99: 30, Max: 30}},
		// n=10: p50 rank 5, p95 rank ceil(9.5)=10, p99 rank 10.
		{"ten with outlier", []int{1, 1, 1, 1, 2, 2, 2, 2, 2, 1000}, Percentiles{Count: 10, Mean: 101.4, P50: 2, P95: 1000, P99: 1000, Max: 1000}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summarize(msDurations(c.in...)); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestSummarizeSubMillisecondRounding(t *testing.T) {
	got := summarize([]time.Duration{1234567 * time.Nanosecond})
	if got.P50 != 1.23 || got.Max != 1.23 {
		t.Fatalf("1.234567ms rounded to %v / %v, want 1.23", got.P50, got.Max)
	}
}

func TestReportNotes(t *testing.T) {
	r := &Report{
		Setup:   SetupReport{Streams: 10, PerMemberRate: 2.5},
		Sends:   SendReport{RateLimited: 3, Skipped: 2},
		Deliver: DeliverReport{Lost: 4},
		Streams: StreamReport{OpenAtStart: 9},
	}
	notes := strings.Join(r.notes(), "\n")
	for _, want := range []string{"per-subject chat limit bound: 3 sends got 429", "2 sends were skipped", "4 expected deliveries never arrived", "only 9 of 10 streams"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q:\n%s", want, notes)
		}
	}
	if n := (&Report{Setup: SetupReport{Streams: 1}, Streams: StreamReport{OpenAtStart: 1}}).notes(); len(n) != 0 {
		t.Errorf("clean run has notes: %v", n)
	}
}

func TestReportOutputs(t *testing.T) {
	r := &Report{
		Gateway: "http://gw",
		Sends:   SendReport{Attempted: 5, OK: 4, RateLimited: 1, Errors: map[string]int{"429": 1, "error: timeout": 2}},
		Deliver: DeliverReport{LatencyMs: Percentiles{Count: 2, P50: 1, P95: 2, P99: 2, Max: 2, Mean: 1.5}},
	}
	var text bytes.Buffer
	r.writeText(&text)
	for _, want := range []string{"rate-limited (429) 1", "errors: 429=1, error: timeout=2", "p50 1.0  p95 2.0  p99 2.0  max 2.0"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text report missing %q:\n%s", want, text.String())
		}
	}
	var js bytes.Buffer
	if err := r.writeJSON(&js); err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(js.Bytes(), &back); err != nil {
		t.Fatalf("json report does not round-trip: %v", err)
	}
	if back.Sends.Errors["error: timeout"] != 2 || back.Deliver.LatencyMs.P95 != 2 {
		t.Fatalf("json round-trip lost fields: %+v", back)
	}
}
