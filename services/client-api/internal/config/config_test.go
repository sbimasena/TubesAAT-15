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
	cfg, err := Load(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.AggregatorRequestTimeout != 1500*time.Millisecond || !cfg.EnableProvisionalHazards {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsMissingAggregatorURL(t *testing.T) {
	_, err := Load(func(key string) string {
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
	_, err := Load(func(key string) string {
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
		cfg, err := Load(func(key string) string {
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
		if cfg.WriteTimeout <= cfg.AggregatorRequestTimeout || cfg.WriteTimeout < 15*time.Second {
			t.Fatalf("server write timeout must allow the dependency error response: %+v", cfg)
		}
	}
}

func TestLoadRejectsTimeoutOverflow(t *testing.T) {
	_, err := Load(func(key string) string {
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
