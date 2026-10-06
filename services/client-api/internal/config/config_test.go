package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadClientConfig(t *testing.T) {
	values := map[string]string{
		"AGGREGATOR_BASE_URL":                "http://aggregator:8083",
		"AGGREGATOR_REQUEST_TIMEOUT_MS":      "1500",
		"ENABLE_PROVISIONAL_HAZARD_ENDPOINT": "true",
	}
	cfg, err := loadForTest(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.AggregatorRequestTimeout != 1500*time.Millisecond || !cfg.EnableProvisionalHazards || cfg.MaxConcurrentRequests != 64 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConcurrencyBound(t *testing.T) {
	for _, value := range []string{"1", "128", "0", "-1", "1.5", "unbounded", "99999999999999999999999"} {
		values := map[string]string{"AGGREGATOR_BASE_URL": "http://aggregator:8083",
			"AGGREGATOR_REQUEST_TIMEOUT_MS": "5000", "CLIENT_API_MAX_CONCURRENT_REQUESTS": value}
		cfg, err := loadForTest(func(key string) string { return values[key] })
		if value == "1" || value == "128" {
			if err != nil || cfg.MaxConcurrentRequests < 1 {
				t.Fatal("valid configurable limit rejected")
			}
		} else if err == nil || !strings.Contains(err.Error(), "CLIENT_API_MAX_CONCURRENT_REQUESTS") {
			t.Fatal("invalid concurrency limit accepted")
		}
	}
}

func TestLoadRejectsMissingAggregatorURL(t *testing.T) {
	_, err := loadForTest(func(key string) string {
		if key == "AGGREGATOR_REQUEST_TIMEOUT_MS" {
			return "1000"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "AGGREGATOR_BASE_URL") {
		t.Fatalf("expected Aggregator URL error, got %v", err)
	}
}

func TestLoadRejectsMissingTimeout(t *testing.T) {
	_, err := loadForTest(func(key string) string {
		if key == "AGGREGATOR_BASE_URL" {
			return "http://aggregator:8083"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "AGGREGATOR_REQUEST_TIMEOUT_MS") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestWriteTimeoutAllowsDependencyFailureResponse(t *testing.T) {
	for _, milliseconds := range []string{"1500", "20000"} {
		cfg, err := loadForTest(func(key string) string {
			switch key {
			case "AGGREGATOR_BASE_URL":
				return "http://aggregator:8083"
			case "AGGREGATOR_REQUEST_TIMEOUT_MS":
				return milliseconds
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.WriteTimeout <= cfg.AggregatorRequestTimeout+cfg.AuthRequestTimeout || cfg.WriteTimeout < 15*time.Second {
			t.Fatalf("server write timeout must allow the dependency error response: %+v", cfg)
		}
	}
}

func TestLoadRejectsTimeoutOverflow(t *testing.T) {
	_, err := loadForTest(func(key string) string {
		switch key {
		case "AGGREGATOR_BASE_URL":
			return "http://aggregator:8083"
		case "AGGREGATOR_REQUEST_TIMEOUT_MS":
			return "9223372036854"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected timeout overflow to be rejected")
	}
}

func loadForTest(getenv func(string) string) (Config, error) {
	return Load(func(key string) string {
		switch key {
		case "AUTH_BASE_URL":
			return "http://auth:8084"
		case "AUTH_REQUEST_TIMEOUT_MS":
			return "5000"
		case "AUTH_INTERNAL_SECRET":
			return strings.Repeat("internal-test", 3)
		}
		return getenv(key)
	})
}

func TestLoadRequiresAuthConfiguration(t *testing.T) {
	for _, field := range []string{"AUTH_BASE_URL", "AUTH_INTERNAL_SECRET", "AUTH_REQUEST_TIMEOUT_MS"} {
		values := map[string]string{"AGGREGATOR_BASE_URL": "http://aggregator:8083", "AGGREGATOR_REQUEST_TIMEOUT_MS": "5000",
			"AUTH_BASE_URL": "http://auth:8084", "AUTH_REQUEST_TIMEOUT_MS": "5000", "AUTH_INTERNAL_SECRET": strings.Repeat("test", 8)}
		delete(values, field)
		if _, err := Load(func(key string) string { return values[key] }); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("missing %s accepted: %v", field, err)
		}
	}
}
