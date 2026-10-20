// Package metrics exposes the server's state in the Prometheus text format.
//
// It is written by hand rather than with client_golang: the server needs a few
// counters, gauges and one histogram, and a dependency tree for that is more to
// audit than the format itself. The exposition is checked by a test against the
// rules a Prometheus scraper enforces.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Registry holds every metric and renders them.
type Registry struct {
	mu       sync.Mutex
	counters map[string]*CounterVec
	gauges   []gaugeFunc
	hists    map[string]*HistogramVec
	order    []string
	help     map[string]string
	kind     map[string]string
}

type gaugeFunc struct {
	name   string
	labels map[string]string
	value  func() float64
}

func NewRegistry() *Registry {
	return &Registry{
		counters: map[string]*CounterVec{},
		hists:    map[string]*HistogramVec{},
		help:     map[string]string{},
		kind:     map[string]string{},
	}
}

func (r *Registry) declare(name, help, kind string) {
	if _, ok := r.kind[name]; ok {
		return
	}
	r.order = append(r.order, name)
	r.help[name] = help
	r.kind[name] = kind
}

// CounterVec is a counter with labels, created per label set on first use.
type CounterVec struct {
	name   string
	labels []string
	mu     sync.Mutex
	values map[string]*atomic.Uint64
	keys   map[string][]string
}

func (r *Registry) Counter(name, help string, labels ...string) *CounterVec {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.declare(name, help, "counter")
	c := &CounterVec{name: name, labels: labels, values: map[string]*atomic.Uint64{}, keys: map[string][]string{}}
	r.counters[name] = c
	return c
}

// Inc adds one for the given label values, in the order the labels were
// declared.
func (c *CounterVec) Inc(values ...string) { c.Add(1, values...) }

func (c *CounterVec) Add(n uint64, values ...string) {
	key := strings.Join(values, "\x00")
	c.mu.Lock()
	v, ok := c.values[key]
	if !ok {
		v = &atomic.Uint64{}
		c.values[key] = v
		c.keys[key] = append([]string(nil), values...)
	}
	c.mu.Unlock()
	v.Add(n)
}

// GaugeFunc registers a value read at scrape time. Gauges that mirror state the
// server already holds (connected agents, pool stats) are read rather than kept
// in step, so they can never drift from what they describe.
func (r *Registry) GaugeFunc(name, help string, labels map[string]string, value func() float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.declare(name, help, "gauge")
	r.gauges = append(r.gauges, gaugeFunc{name: name, labels: labels, value: value})
}

// HistogramVec is a histogram with labels and fixed buckets.
type HistogramVec struct {
	name    string
	labels  []string
	buckets []float64
	mu      sync.Mutex
	series  map[string]*histSeries
}

type histSeries struct {
	values []string
	counts []uint64 // per bucket, not cumulative
	count  uint64
	sum    float64
}

// DefaultLatencyBuckets suits HTTP handlers: 5ms to 10s.
var DefaultLatencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *HistogramVec {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.declare(name, help, "histogram")
	h := &HistogramVec{name: name, labels: labels, buckets: buckets, series: map[string]*histSeries{}}
	r.hists[name] = h
	return h
}

func (h *HistogramVec) Observe(v float64, values ...string) {
	key := strings.Join(values, "\x00")
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.series[key]
	if !ok {
		s = &histSeries{values: append([]string(nil), values...), counts: make([]uint64, len(h.buckets))}
		h.series[key] = s
	}
	for i, b := range h.buckets {
		if v <= b {
			s.counts[i]++
			break
		}
	}
	s.count++
	s.sum += v
}

// Write renders every metric in the Prometheus text exposition format.
func (r *Registry) Write(w io.Writer) error {
	r.mu.Lock()
	order := append([]string(nil), r.order...)
	gauges := append([]gaugeFunc(nil), r.gauges...)
	r.mu.Unlock()

	var b strings.Builder
	for _, name := range order {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(r.help[name]), name, r.kind[name])
		switch r.kind[name] {
		case "counter":
			c := r.counters[name]
			c.mu.Lock()
			keys := make([]string, 0, len(c.values))
			for k := range c.values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "%s%s %d\n", name, labelString(c.labels, c.keys[k], nil), c.values[k].Load())
			}
			c.mu.Unlock()
		case "gauge":
			for _, g := range gauges {
				if g.name != name {
					continue
				}
				fmt.Fprintf(&b, "%s%s %s\n", name, mapLabels(g.labels), formatFloat(g.value()))
			}
		case "histogram":
			h := r.hists[name]
			h.mu.Lock()
			keys := make([]string, 0, len(h.series))
			for k := range h.series {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				s := h.series[k]
				var cum uint64
				for i, ub := range h.buckets {
					cum += s.counts[i]
					fmt.Fprintf(&b, "%s_bucket%s %d\n", name, labelString(h.labels, s.values, &bucketLabel{formatFloat(ub)}), cum)
				}
				fmt.Fprintf(&b, "%s_bucket%s %d\n", name, labelString(h.labels, s.values, &bucketLabel{"+Inf"}), s.count)
				fmt.Fprintf(&b, "%s_sum%s %s\n", name, labelString(h.labels, s.values, nil), formatFloat(s.sum))
				fmt.Fprintf(&b, "%s_count%s %d\n", name, labelString(h.labels, s.values, nil), s.count)
			}
			h.mu.Unlock()
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

type bucketLabel struct{ le string }

func labelString(names, values []string, le *bucketLabel) string {
	if len(names) == 0 && le == nil {
		return ""
	}
	parts := make([]string, 0, len(names)+1)
	for i, n := range names {
		v := ""
		if i < len(values) {
			v = values[i]
		}
		parts = append(parts, n+`="`+escapeLabel(v)+`"`)
	}
	if le != nil {
		parts = append(parts, `le="`+le.le+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func mapLabels(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	values := make([]string, len(names))
	for i, n := range names {
		values[i] = m[n]
	}
	return labelString(names, values, nil)
}

func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return strings.ReplaceAll(v, `"`, `\"`)
}

func escapeHelp(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(v, "\n", `\n`)
}

func formatFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	case math.IsNaN(f):
		return "NaN"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
