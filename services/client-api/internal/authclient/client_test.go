package authclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIntrospectionHasNoCacheAndPropagatesCorrelationWithoutLoggingSecrets(t *testing.T) {
	secret := strings.Repeat("internal-test", 3)
	var logs bytes.Buffer
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/internal/v1/introspect" ||
			r.Header.Get("X-Auth-Service-Key") != secret || r.Header.Get("X-Correlation-ID") != "test-correlation" {
			t.Error("incorrect introspection request")
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["token"] != "test-only-access" {
			t.Error("token not sent")
		}
		if calls == 1 {
			_, _ = w.Write([]byte(`{"active":true,"client_id":"media","scope":"media","session_id":"sid"}`))
		} else {
			_, _ = w.Write([]byte(`{"active":false}`))
		}
	}))
	defer server.Close()
	client, _ := New(server.URL, secret, time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
	identity, err := client.Validate(context.Background(), "test-only-access", "test-correlation")
	if err != nil || identity.Scope != "media" {
		t.Fatalf("valid identity rejected: %v", err)
	}
	if _, err := client.Validate(context.Background(), "test-only-access", "test-correlation"); !errors.Is(err, ErrInvalid) || calls != 2 {
		t.Fatal("revocation hidden by caching")
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "test-only-access") || !strings.Contains(logs.String(), "latency_ms") {
		t.Fatal("unsafe or missing outbound logs")
	}
}

func TestDependencyFailuresAndInvalidResponsesFailClosed(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		kind       string
	}{
		{"internal credential mismatch", "", 401, "upstream_status"}, {"server unavailable", "", 503, "upstream_status"},
		{"malformed", "bad JSON", 200, "invalid_response"}, {"missing active", `{}`, 200, "invalid_response"},
		{"invalid scope", `{"active":true,"client_id":"x","scope":"root","session_id":"s"}`, 200, "invalid_response"},
		{"oversized", strings.Repeat("x", 8193), 200, "invalid_response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			var logs bytes.Buffer
			client, _ := New(server.URL, strings.Repeat("test", 8), time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
			if _, err := client.Validate(context.Background(), "access", "corr"); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("expected unavailable, got %v", err)
			}
			var record map[string]any
			if json.Unmarshal(logs.Bytes(), &record) != nil || record["level"] != "WARN" ||
				record["error_kind"] != test.kind || record["target"] != "auth" || record["status"] != float64(test.status) ||
				record["correlation_id"] != "corr" || record["latency_ms"] == nil || record["client_id"] != nil {
				t.Fatal("Auth dependency failure lacks a safe classified outbound log")
			}
		})
	}
}

func TestTimeoutCancellationRefusedConnectionAndRedirect(t *testing.T) {
	for _, mode := range []string{"header timeout", "body timeout", "canceled", "refused", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if mode == "redirect" {
					http.Redirect(w, r, "/other", http.StatusFound)
					return
				}
				if mode == "body timeout" {
					_, _ = w.Write([]byte(`{"active":`))
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			if mode == "refused" {
				server.Close()
			}
			var logs bytes.Buffer
			client, _ := New(server.URL, strings.Repeat("test", 8), 30*time.Millisecond, slog.New(slog.NewJSONHandler(&logs, nil)))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if _, err := client.Validate(ctx, "access", "corr"); !errors.Is(err, ErrUnavailable) {
				t.Fatal("dependency failure accepted")
			}
			kind := "timeout"
			switch mode {
			case "canceled":
				kind = "canceled"
			case "refused":
				kind = "transport"
			case "redirect":
				kind = "upstream_status"
			}
			if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"error_kind":"`+kind+`"`) {
				t.Fatal("transport failure log missing classification")
			}
		})
	}
}
