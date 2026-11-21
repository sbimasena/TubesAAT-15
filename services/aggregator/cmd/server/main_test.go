package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCoastalEnvironmentConfiguration(t *testing.T) {
	t.Setenv("PANTAI_BASE_URL", "http://sensor-pantai:8080")
	t.Setenv("PANTAI_AUTH_TOKEN", "temporary-token")
	t.Setenv("PANTAI_PAGE_LIMIT", "25")
	t.Setenv("PANTAI_REQUEST_TIMEOUT_MS", "500")
	config, err := coastalConfigFromEnv()
	if err != nil || config.PageLimit != 25 || config.Timeout != 500*time.Millisecond || config.Token != "temporary-token" {
		t.Fatalf("coastal env: %+v %v", config, err)
	}
	for _, value := range []string{"0", "-1", "not-a-number", "9223372036854775807"} {
		t.Setenv("PANTAI_REQUEST_TIMEOUT_MS", value)
		if _, err := coastalConfigFromEnv(); err == nil {
			t.Fatal("invalid coastal duration accepted")
		}
	}
}

func TestCoastalResilienceEnvironment(t *testing.T) {
	t.Setenv("PANTAI_BREAKER_FAILURE_THRESHOLD", "4")
	t.Setenv("PANTAI_BREAKER_OPEN_MS", "12000")
	t.Setenv("PANTAI_MAX_ATTEMPTS", "3")
	t.Setenv("PANTAI_RETRY_WINDOW_MS", "15000")
	t.Setenv("PANTAI_RETRIES_PER_WINDOW", "0")
	config, err := coastalConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	got := config.Resilience
	if got.FailureThreshold != 4 || got.OpenFor != 12*time.Second || got.MaxAttempts != 3 ||
		got.RetryWindow != 15*time.Second || got.RetriesPerWindow != 0 ||
		got.RetryBase != 250*time.Millisecond || got.RetryMax != 2*time.Second {
		t.Fatal("unexpected Pantai resilience settings")
	}
	for name, invalid := range map[string]string{
		"PANTAI_BREAKER_FAILURE_THRESHOLD": "0",
		"PANTAI_BREAKER_OPEN_MS":           "9223372036854775807",
		"PANTAI_MAX_ATTEMPTS":              "11",
		"PANTAI_RETRY_WINDOW_MS":           "0",
		"PANTAI_RETRIES_PER_WINDOW":        "-1",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, invalid)
			if _, err := coastalConfigFromEnv(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("invalid %s: expected a named configuration error, got %v", name, err)
			}
		})
	}
}

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
