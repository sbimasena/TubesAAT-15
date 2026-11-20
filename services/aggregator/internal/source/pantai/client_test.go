package pantai

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, server *httptest.Server, configure func(*Config)) *Client {
	t.Helper()
	config := Config{BaseURL: server.URL, Token: "private-token", InstanceID: "replica-one"}
	if configure != nil {
		configure(&config)
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestHTTPContractAndSecretSafeLogs(t *testing.T) {
	var logs bytes.Buffer
	var verified atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("X-Correlation-ID") != "corr-test" || r.Header.Get("X-Instance-ID") != "replica-one" {
			t.Error("missing auth, correlation or instance headers")
		}
		if r.URL.Query().Get("cursor") != "opaque/+==" || r.URL.Query().Get("limit") != "17" {
			t.Error("query values changed")
		}
		w.Write(envelope(validObservation))
	}))
	defer server.Close()
	client := newTestClient(t, server, func(c *Config) {
		c.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		c.Verify = func(context.Context) error { verified.Add(1); return nil }
	})
	batch, err := client.Observations(context.Background(), "opaque/+==", 17, "corr-test")
	if err != nil || batch.Invalid != nil || len(batch.Records) != 1 || verified.Load() != 1 {
		t.Fatalf("request failed: batch=%+v err=%v", batch, err)
	}
	if !strings.Contains(logs.String(), `"latency_ms"`) || !strings.Contains(logs.String(), "corr-test") || strings.Contains(logs.String(), "private-token") || strings.Contains(logs.String(), "independent-id") {
		t.Fatalf("incorrect outbound log: %s", logs.String())
	}
}

func TestHTTPRejectsRedirectAndPropagatesStatus(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer target.Close()
	for _, status := range []int{302, 401, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL)
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(status)
				w.Write([]byte("sensitive error body"))
			}))
			defer server.Close()
			client := newTestClient(t, server, nil)
			_, err := client.Observations(context.Background(), "", 10, "corr")
			var upstream *HTTPError
			if !errors.As(err, &upstream) || upstream.StatusCode != status || upstream.RetryAfter != "7" || calls.Load() != 1 {
				t.Fatalf("status/retry contract: %v calls=%d", err, calls.Load())
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatal("error body exposed")
			}
		})
	}
	if leaked.Load() != 0 {
		t.Fatal("redirect destination received a request")
	}
}

func TestHTTPDeadlineCancellationAndLeaseLoss(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, func(c *Config) { c.Timeout = 25 * time.Millisecond })
	if _, err := client.Observations(context.Background(), "", 10, "corr"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not propagated: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Observations(ctx, "", 10, "corr"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	lost := errors.New("leader term lost")
	before := calls.Load()
	client = newTestClient(t, server, func(c *Config) { c.Verify = func(context.Context) error { return lost } })
	if _, err := client.Observations(context.Background(), "", 10, "corr"); !errors.Is(err, lost) || calls.Load() != before {
		t.Fatal("upstream called after lease verification failed")
	}
}

func TestHTTPResponseCaptureLimits(t *testing.T) {
	for _, size := range []int{responseLimit + 1, captureLimit + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(bytes.Repeat([]byte("x"), size))
			}))
			defer server.Close()
			client := newTestClient(t, server, nil)
			batch, err := client.Observations(context.Background(), "", 10, "corr")
			if err != nil || batch.Invalid == nil || batch.Truncated != (size > captureLimit) {
				t.Fatalf("capture boundary failed: truncated=%v invalid=%v err=%v", batch.Truncated, batch.Invalid, err)
			}
			if size <= captureLimit && len(batch.Raw) != size || size > captureLimit && len(batch.Raw) != 0 {
				t.Fatal("partial bytes presented as complete payload")
			}
		})
	}
}

func TestConfigRejectsInvalidCredentialsAndEndpoints(t *testing.T) {
	for _, baseURL := range []string{"", "file:///tmp/a", "http://user:password@localhost", "http://localhost?q=x", "http://localhost/#fragment"} {
		if _, err := New(Config{BaseURL: baseURL, Token: "token"}); err == nil {
			t.Fatalf("invalid endpoint accepted: %s", baseURL)
		}
	}
	for _, token := range []string{"", " ", "x\r\ny"} {
		if _, err := New(Config{BaseURL: "http://localhost", Token: token}); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}

// The official image is exercised over HTTP only; no practice IDs or seeds
// participate in ingestion decisions. This is a contract sample, not a game day.
func TestOfficialSensorContract(t *testing.T) {
	endpoint := os.Getenv("TEST_PANTAI_URL")
	if endpoint == "" {
		t.Skip("set TEST_PANTAI_URL and TEST_PANTAI_TOKEN for the official image")
	}
	client, err := New(Config{BaseURL: endpoint, Token: os.Getenv("TEST_PANTAI_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stations, err := client.Stations(ctx, "official-contract")
	if err != nil || len(stations) == 0 {
		t.Fatalf("official stations: %v", err)
	}
	for ctx.Err() == nil {
		batch, err := client.Observations(ctx, "", 100, "official-contract")
		if err != nil || batch.Invalid != nil {
			t.Fatalf("official envelope: err=%v invalid=%v", err, batch.Invalid)
		}
		for _, record := range batch.Records {
			if record.Invalid != nil {
				t.Fatalf("initial official record failed structural validation: %v", record.Invalid)
			}
		}
		if len(batch.Records) != 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("official sensor produced no observations in the sampling window")
}
