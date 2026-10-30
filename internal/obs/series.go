package obs

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Labeled domain series: counters and gauges a service records beside the
// HTTP request metrics (SSE connections, published events, notification
// delivery outcomes, bridge heartbeats, borrow decisions). Every series is
// rendered as bnbloc_<name>{service="…",<labels>} in the same registry as the
// request metrics.
//
// Cardinality is the caller's contract: a label value must come from a fixed,
// code-owned set (an outcome enum, a known event kind, "other" for anything
// else) — never from a request, an id, or a user-chosen string.

// Label is one series label. Values must be drawn from a bounded set.
type Label struct {
	Name  string
	Value string
}

// L builds a Label.
func L(name, value string) Label { return Label{Name: name, Value: value} }

type seriesType string

const (
	seriesCounter   seriesType = "counter"
	seriesGauge     seriesType = "gauge"
	seriesHistogram seriesType = "histogram"
)

// histogramBucketsSec are the cumulative upper bounds (seconds) of every
// domain histogram: a millisecond to ten seconds.
var histogramBucketsSec = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// family is one metric name with its type, help text and samples.
type family struct {
	typ     seriesType
	help    string
	samples map[string]*sample // keyed by the rendered label set
}

type sample struct {
	labels string // rendered `,k="v"...` suffix (sorted by label name)
	value  float64
	// A histogram's bucket counts (histogramBucketsSec) and observation
	// count; value is its sum.
	buckets []uint64
	count   uint64
}

// Describe sets the HELP text of a domain series (optional; rendered once).
func (m *Metrics) Describe(name, help string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.familyLocked(name, ""); f != nil {
		f.help = help
	}
}

// CounterAdd adds v (>= 0) to the counter name{labels}. Adding 0 registers
// the series so it renders before its first event (rate() and absence
// alerts need a zero sample, not a missing one).
func (m *Metrics) CounterAdd(name string, v float64, labels ...Label) {
	if m == nil || v < 0 {
		return
	}
	m.update(name, seriesCounter, labels, func(s *sample) { s.value += v })
}

// CounterInc increments the counter name{labels}.
func (m *Metrics) CounterInc(name string, labels ...Label) { m.CounterAdd(name, 1, labels...) }

// GaugeSet sets the gauge name{labels} to v.
func (m *Metrics) GaugeSet(name string, v float64, labels ...Label) {
	if m == nil {
		return
	}
	m.update(name, seriesGauge, labels, func(s *sample) { s.value = v })
}

// GaugeAdd moves the gauge name{labels} by d (negative to decrease).
func (m *Metrics) GaugeAdd(name string, d float64, labels ...Label) {
	if m == nil {
		return
	}
	m.update(name, seriesGauge, labels, func(s *sample) { s.value += d })
}

// HistogramObserve records one observation (seconds) in the histogram
// name{labels}.
func (m *Metrics) HistogramObserve(name string, seconds float64, labels ...Label) {
	if m == nil || seconds < 0 {
		return
	}
	m.update(name, seriesHistogram, labels, func(s *sample) {
		if s.buckets == nil {
			s.buckets = make([]uint64, len(histogramBucketsSec))
		}
		for i, b := range histogramBucketsSec {
			if seconds <= b {
				s.buckets[i]++
			}
		}
		s.count++
		s.value += seconds
	})
}

func (m *Metrics) update(name string, typ seriesType, labels []Label, fn func(*sample)) {
	key := renderLabels(labels)
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.familyLocked(name, typ)
	if f == nil || (f.typ != "" && f.typ != typ) {
		// A name is one type for the process lifetime; a mismatched update is
		// a programming error and is dropped rather than corrupting the
		// exposition.
		return
	}
	f.typ = typ
	s := f.samples[key]
	if s == nil {
		s = &sample{labels: key}
		f.samples[key] = s
	}
	fn(s)
}

// familyLocked returns (creating) the family; m.mu is held. typ "" leaves a
// new family's type unset until its first update.
func (m *Metrics) familyLocked(name string, typ seriesType) *family {
	if name == "" {
		return nil
	}
	if m.families == nil {
		m.families = map[string]*family{}
	}
	f := m.families[name]
	if f == nil {
		f = &family{typ: typ, samples: map[string]*sample{}}
		m.families[name] = f
	}
	return f
}

// renderLabels renders labels as a sorted `,name="value"` suffix.
func renderLabels(labels []Label) string {
	if len(labels) == 0 {
		return ""
	}
	sorted := append([]Label(nil), labels...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var b strings.Builder
	for _, l := range sorted {
		fmt.Fprintf(&b, ",%s=%s", l.Name, quoteLabel(l.Value))
	}
	return b.String()
}

// quoteLabel escapes a label value per the Prometheus text format
// (backslash, double quote, newline).
func quoteLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return `"` + v + `"`
}

// writeSeriesLocked renders every domain family; m.mu is held.
func (m *Metrics) writeSeriesLocked(w io.Writer) {
	names := make([]string, 0, len(m.families))
	for n, f := range m.families {
		if f.typ != "" && len(f.samples) > 0 {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		f := m.families[n]
		metric := "bnbloc_" + n
		if f.help != "" {
			fmt.Fprintf(w, "# HELP %s %s\n", metric, f.help)
		}
		fmt.Fprintf(w, "# TYPE %s %s\n", metric, f.typ)
		keys := make([]string, 0, len(f.samples))
		for k := range f.samples {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			smp := f.samples[k]
			if f.typ == seriesHistogram {
				for i, b := range histogramBucketsSec {
					fmt.Fprintf(w, "%s_bucket{service=%q%s,le=%q} %d\n", metric, m.service, k, strconv.FormatFloat(b, 'f', -1, 64), smp.buckets[i])
				}
				fmt.Fprintf(w, "%s_bucket{service=%q%s,le=\"+Inf\"} %d\n", metric, m.service, k, smp.count)
				fmt.Fprintf(w, "%s_sum{service=%q%s} %s\n", metric, m.service, k, strconv.FormatFloat(smp.value, 'f', -1, 64))
				fmt.Fprintf(w, "%s_count{service=%q%s} %d\n", metric, m.service, k, smp.count)
				continue
			}
			fmt.Fprintf(w, "%s{service=%q%s} %s\n", metric, m.service, k,
				strconv.FormatFloat(smp.value, 'f', -1, 64))
		}
	}
}
