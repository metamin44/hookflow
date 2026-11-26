package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestMetricsHandler(t *testing.T) {
	metrics := New()
	metrics.Set("hookflow_broker_ready", 1)
	metrics.Add("hookflow_publisher_confirmed_total", 2)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"# TYPE hookflow_broker_ready gauge",
		"hookflow_broker_ready 1",
		"# TYPE hookflow_publisher_confirmed_total counter",
		"hookflow_publisher_confirmed_total 2",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q: %s", want, body)
		}
	}
}

func TestMetricsConcurrentAdd(t *testing.T) {
	metrics := New()
	metrics.Set("hookflow_worker_completed_total", 0)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 1000 {
				metrics.Add("hookflow_worker_completed_total", 1)
			}
		}()
	}
	wait.Wait()
	if got := metrics.render(); !strings.Contains(got, "hookflow_worker_completed_total 8000") {
		t.Fatalf("unexpected metrics: %s", got)
	}
}

func TestMetricsRejectsPost(t *testing.T) {
	recorder := httptest.NewRecorder()
	New().Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}
