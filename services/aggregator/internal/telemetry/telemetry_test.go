package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMiddlewareLabelsByRoutePattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	})
	handler := Middleware("test", mux)
	for _, path := range []string{"/items/1", "/items/2", "/nope"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if got := testutil.ToFloat64(requests.WithLabelValues("GET /items/{id}", "GET", "410")); got != 2 {
		t.Fatalf("two paths under one pattern must share one series, got %v", got)
	}
	if got := testutil.ToFloat64(requests.WithLabelValues("unmatched", "GET", "404")); got != 1 {
		t.Fatalf("unrouted request must count as unmatched/404, got %v", got)
	}
}
