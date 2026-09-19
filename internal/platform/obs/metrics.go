package obs

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Metrics is a small in-process registry exposed in Prometheus text format.
// Labels are bounded (route template, method, status class, outcome); tenant,
// user and entity IDs never appear.
type Metrics struct {
	mu        sync.Mutex
	requests  map[string]int64   // route|method|class
	latency   map[string][]int64 // route -> bucket counts
	counters  map[string]int64   // named events
	buckets   []float64
	startedAt time.Time
}

var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// NewMetrics creates a registry.
func NewMetrics() *Metrics {
	return &Metrics{requests: map[string]int64{}, latency: map[string][]int64{}, counters: map[string]int64{}, buckets: defaultBuckets, startedAt: time.Now()}
}

// ObserveRequest records one HTTP request.
func (m *Metrics) ObserveRequest(route, method string, status int, took time.Duration) {
	if m == nil {
		return
	}
	class := fmt.Sprintf("%dxx", status/100)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[route+"|"+method+"|"+class]++
	b, ok := m.latency[route]
	if !ok {
		b = make([]int64, len(m.buckets)+1)
		m.latency[route] = b
	}
	sec := took.Seconds()
	placed := false
	for i, ub := range m.buckets {
		if sec <= ub {
			b[i]++
			placed = true
			break
		}
	}
	if !placed {
		b[len(m.buckets)]++
	}
}

// Inc increments a named counter (e.g. write_conflicts, auth_failures, rate_limited).
func (m *Metrics) Inc(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.counters[name]++
	m.mu.Unlock()
}

// Write renders the registry in Prometheus exposition format.
func (m *Metrics) Write(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fmt.Fprintf(w, "# TYPE tmp_uptime_seconds gauge\ntmp_uptime_seconds %d\n", int64(time.Since(m.startedAt).Seconds()))
	fmt.Fprintln(w, "# TYPE tmp_http_requests_total counter")
	keys := make([]string, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var route, method, class string
		fmt.Sscanf(strings3(k), "%s %s %s", &route, &method, &class)
		fmt.Fprintf(w, "tmp_http_requests_total{route=%q,method=%q,class=%q} %d\n", route, method, class, m.requests[k])
	}
	fmt.Fprintln(w, "# TYPE tmp_http_request_duration_seconds histogram")
	routes := make([]string, 0, len(m.latency))
	for r := range m.latency {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	for _, r := range routes {
		var cum int64
		for i, ub := range m.buckets {
			cum += m.latency[r][i]
			fmt.Fprintf(w, "tmp_http_request_duration_seconds_bucket{route=%q,le=\"%g\"} %d\n", r, ub, cum)
		}
		cum += m.latency[r][len(m.buckets)]
		fmt.Fprintf(w, "tmp_http_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\n", r, cum)
		fmt.Fprintf(w, "tmp_http_request_duration_seconds_count{route=%q} %d\n", r, cum)
	}
	names := make([]string, 0, len(m.counters))
	for n := range m.counters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "# TYPE tmp_%s_total counter\ntmp_%s_total %d\n", n, n, m.counters[n])
	}
}

// strings3 turns "a|b|c" into "a b c" for Sscanf without importing strings here.
func strings3(k string) string {
	out := []byte(k)
	for i := range out {
		if out[i] == '|' {
			out[i] = ' '
		}
	}
	return string(out)
}

// Default is the process registry.
var Default = NewMetrics()
