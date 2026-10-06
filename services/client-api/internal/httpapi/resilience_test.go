package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/authclient"
)

type validatorFunc func(context.Context, string, string) (authclient.Identity, error)

func (f validatorFunc) Validate(ctx context.Context, access, correlation string) (authclient.Identity, error) {
	return f(ctx, access, correlation)
}

type listerFunc func(context.Context, url.Values, string) ([]byte, error)

func (f listerFunc) List(ctx context.Context, query url.Values, correlation string) ([]byte, error) {
	return f(ctx, query, correlation)
}

func assertAPIError(t *testing.T, w *httptest.ResponseRecorder, status int, code, correlation string) {
	t.Helper()
	var payload map[string]json.RawMessage
	var detail map[string]string
	var id string
	if w.Code != status || w.Header().Get("Content-Type") != "application/json" ||
		w.Header().Get("X-Correlation-ID") != correlation || w.Header().Get("Cache-Control") != "no-store" ||
		json.Unmarshal(w.Body.Bytes(), &payload) != nil || len(payload) != 2 ||
		json.Unmarshal(payload["error"], &detail) != nil || len(detail) != 2 || detail["code"] != code || detail["message"] == "" ||
		json.Unmarshal(payload["correlation_id"], &id) != nil || id != correlation {
		t.Fatalf("unexpected API error contract: status=%d body=%s", w.Code, w.Body.String())
	}
}

func hazardRequest(path string) *http.Request {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer test-access")
	r.Header.Set("X-Correlation-ID", "resilience-test")
	return r
}

func TestConcurrencyBoundsAuthAndAggregatorAndReleasesCanceledRequests(t *testing.T) {
	for _, stage := range []string{"auth", "aggregator"} {
		t.Run(stage, func(t *testing.T) {
			const capacity = 64
			entered := make(chan struct{}, capacity+1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			wait := func(ctx context.Context) error {
				entered <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			var authCalls, aggregatorCalls atomic.Int64
			validator := validatorFunc(func(ctx context.Context, _, _ string) (authclient.Identity, error) {
				authCalls.Add(1)
				if stage == "auth" {
					if err := wait(ctx); err != nil {
						return authclient.Identity{}, err
					}
				}
				return authclient.Identity{ClientID: "ops", Scope: "internal-ops", SessionID: "session"}, nil
			})
			lister := listerFunc(func(ctx context.Context, _ url.Values, _ string) ([]byte, error) {
				aggregatorCalls.Add(1)
				if stage == "aggregator" {
					if err := wait(ctx); err != nil {
						return nil, err
					}
				}
				return []byte(emptyResponse), nil
			})
			var logs bytes.Buffer
			handler := NewHandler(lister, validator, true, capacity, slog.New(slog.NewJSONHandler(&logs, nil)))
			var workers sync.WaitGroup
			responses := make([]*httptest.ResponseRecorder, capacity+1)
			firstDone := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			for i := 0; i < capacity; i++ {
				responses[i] = httptest.NewRecorder()
				workers.Add(1)
				go func(index int) {
					defer workers.Done()
					r := hazardRequest("/hazards")
					if index == 0 {
						r = r.WithContext(ctx)
						defer close(firstDone)
					}
					handler.ServeHTTP(responses[index], r)
				}(i)
			}
			await := func(signal <-chan struct{}) {
				t.Helper()
				select {
				case <-signal:
				case <-time.After(5 * time.Second):
					t.Fatal("request failed to enter or leave the bounded path")
				}
			}
			for i := 0; i < capacity; i++ {
				await(entered)
			}
			overflow := httptest.NewRecorder()
			handler.ServeHTTP(overflow, hazardRequest("/internal/provisional/hazards"))
			assertAPIError(t, overflow, 429, "concurrency_limit", "resilience-test")
			if overflow.Header().Get("Retry-After") != "1" || authCalls.Load() != capacity ||
				(stage == "aggregator" && aggregatorCalls.Load() != capacity) {
				t.Fatal("overflow reached a dependency or lacks a retry hint")
			}
			health := httptest.NewRecorder()
			handler.ServeHTTP(health, httptest.NewRequest("GET", "/health", nil))
			if health.Code != 200 {
				t.Fatal("healthcheck was limited by hazard capacity")
			}
			cancel()
			await(firstDone)
			responses[capacity] = httptest.NewRecorder()
			workers.Add(1)
			go func() {
				defer workers.Done()
				handler.ServeHTTP(responses[capacity], hazardRequest("/hazards"))
			}()
			await(entered)
			unblock()
			workers.Wait()
			if responses[0].Body.Len() != 0 {
				t.Fatal("canceled request attempted to send an error body")
			}
			for i := 1; i <= capacity; i++ {
				if responses[i].Code != 200 {
					t.Fatal("admitted request failed")
				}
			}
			if !strings.Contains(logs.String(), `"error_kind":"concurrency_limit"`) ||
				!strings.Contains(logs.String(), `"error_kind":"canceled"`) || strings.Contains(logs.String(), "test-access") {
				t.Fatal("429/cancellation log missing or unsafe")
			}
		})
	}
}

func TestErrorResponsesReleaseCapacityAndNeverExposeInternalDetails(t *testing.T) {
	for _, test := range []struct {
		query, code string
		authErr     error
		upstreamErr error
		status      int
	}{
		{code: "invalid_access_token", authErr: authclient.ErrInvalid, status: 401},
		{code: "auth_unavailable", authErr: authclient.ErrUnavailable, status: 503},
		{query: "?include_raw=true", code: "forbidden", status: 403},
		{query: "?fields=unknown", code: "invalid_fields", status: 400},
		{code: "aggregator_unavailable", upstreamErr: &aggregator.RequestError{Kind: aggregator.ErrorUpstreamStatus, Status: 503, Err: errors.New("private")}, status: 503},
		{code: "aggregator_timeout", upstreamErr: &aggregator.RequestError{Kind: aggregator.ErrorTimeout, Err: context.DeadlineExceeded}, status: 504},
		{code: "aggregator_error", upstreamErr: errors.New("private stack trace"), status: 502},
	} {
		t.Run(test.code, func(t *testing.T) {
			validator := validatorFunc(func(context.Context, string, string) (authclient.Identity, error) {
				return authclient.Identity{ClientID: "media", Scope: "media"}, test.authErr
			})
			lister := listerFunc(func(context.Context, url.Values, string) ([]byte, error) { return nil, test.upstreamErr })
			handler := NewHandler(lister, validator, false, 1, nil)
			for i := 0; i < 2; i++ {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, hazardRequest("/hazards"+test.query))
				assertAPIError(t, w, test.status, test.code, "resilience-test")
				if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "test-access") {
					t.Fatal("internal details exposed")
				}
			}
		})
	}
}
