package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInspectLocalAPI(t *testing.T) {
	status, correlation := http.StatusOK, "inspect-test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/internal/v1/hazards?source=BMKG&limit=5" || r.Header.Get("X-Correlation-ID") != "inspect-test" {
			t.Error("inspection changed the query or correlation ID")
		}
		w.Header().Set("X-Correlation-ID", correlation)
		w.WriteHeader(status)
		w.Write([]byte(`{"count":0,"data":[]}`))
	}))
	defer server.Close()
	t.Setenv("AGGREGATOR_PORT", strings.TrimPrefix(server.URL, "http://127.0.0.1:"))
	var output bytes.Buffer
	path := "/internal/v1/hazards?source=BMKG&limit=5"
	if err := inspect(path, "inspect-test", &output); err != nil || output.String() != `{"count":0,"data":[]}` {
		t.Fatalf("inspect: output=%q, error=%v", output.String(), err)
	}
	status = http.StatusServiceUnavailable
	if err := inspect(path, "inspect-test", &output); err == nil {
		t.Fatal("inspection accepted a failed API response")
	}
	status, correlation = http.StatusOK, "changed"
	if err := inspect(path, "inspect-test", &output); err == nil {
		t.Fatal("inspection accepted a changed correlation ID")
	}
	for _, invalid := range []string{"http://example.com/health", "//example.com/health", "/admin/outage", "%"} {
		if err := inspect(invalid, "inspect-test", &output); err == nil {
			t.Errorf("inspection accepted %q", invalid)
		}
	}
}
