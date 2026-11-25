// Package observability provides process-local, low-cardinality runtime metrics.
package observability

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Metrics contains only fixed labels. Entity identifiers, credentials and
// payloads must never be used as metric labels.
type Metrics struct {
	mu     sync.RWMutex
	values map[string]*atomic.Int64
}

// New creates an empty registry.
func New() *Metrics {
	return &Metrics{values: make(map[string]*atomic.Int64)}
}

// Set records a gauge value.
func (m *Metrics) Set(name string, value int64) {
	if m == nil {
		return
	}
	m.counter(name).Store(value)
}

// Add adds to a counter or gauge.
func (m *Metrics) Add(name string, delta int64) {
	if m == nil {
		return
	}
	m.counter(name).Add(delta)
}

func (m *Metrics) counter(name string) *atomic.Int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, exists := m.values[name]
	if !exists {
		value = &atomic.Int64{}
		m.values[name] = value
	}
	return value
}

// Handler returns a Prometheus text exposition handler without entity labels.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, m.render())
	})
}

func (m *Metrics) render() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	names := make([]string, 0, len(m.values))
	values := make(map[string]*atomic.Int64, len(m.values))
	for name, value := range m.values {
		names = append(names, name)
		values[name] = value
	}
	m.mu.RUnlock()
	sort.Strings(names)

	var out strings.Builder
	for _, name := range names {
		kind := "counter"
		if strings.HasSuffix(name, "_in_flight") || strings.HasSuffix(name, "_ready") || strings.HasSuffix(name, "_depth") {
			kind = "gauge"
		}
		fmt.Fprintf(&out, "# TYPE %s %s\n%s %d\n", name, kind, name, values[name].Load())
	}
	return out.String()
}
