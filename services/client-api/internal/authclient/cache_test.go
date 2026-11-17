package authclient

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIntrospectionCache(t *testing.T) {
	secret := strings.Repeat("internal-test", 3)
	valid := `{"active":true,"client_id":"media","scope":"media","session_id":"sid"}`

	t.Run("disabled by default (every request introspects)", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			_, _ = w.Write([]byte(valid))
		}))
		defer server.Close()
		client, _ := New(server.URL, secret, time.Second, 0, nil)
		for i := 0; i < 3; i++ {
			if _, err := client.Validate(context.Background(), "test-token", "corr"); err != nil {
				t.Fatalf("validation failed: %v", err)
			}
		}
		if calls != 3 {
			t.Fatalf("cache unexpectedly active: %d introspections", calls)
		}
	})

	t.Run("valid identity cached within TTL", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			_, _ = w.Write([]byte(valid))
		}))
		defer server.Close()
		client, _ := New(server.URL, secret, time.Second, 50*time.Millisecond, nil)
		identity, err := client.Validate(context.Background(), "test-token", "corr")
		if err != nil || identity.Scope != "media" {
			t.Fatalf("first validation failed: %v", err)
		}
		cached, err := client.Validate(context.Background(), "test-token", "corr")
		if err != nil || cached != identity || calls != 1 {
			t.Fatalf("cache hit failed: calls=%d identity=%v err=%v", calls, cached, err)
		}
		time.Sleep(60 * time.Millisecond)
		if _, err := client.Validate(context.Background(), "test-token", "corr"); err != nil || calls != 2 {
			t.Fatalf("expired entry not refreshed: calls=%d err=%v", calls, err)
		}
	})

	t.Run("rejections and failures are never cached", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			_, _ = w.Write([]byte(`{"active":false}`))
		}))
		defer server.Close()
		client, _ := New(server.URL, secret, time.Second, time.Minute, nil)
		for i := 0; i < 2; i++ {
			if _, err := client.Validate(context.Background(), "test-token", "corr"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected invalid, got %v", err)
			}
		}
		if calls != 2 {
			t.Fatalf("rejection cached: %d introspections", calls)
		}
	})
}

func TestIntrospectionCacheLogRedactsToken(t *testing.T) {
	secret := strings.Repeat("internal-test", 3)
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"active":true,"client_id":"media","scope":"media","session_id":"sid"}`))
	}))
	defer server.Close()
	client, _ := New(server.URL, secret, time.Second, time.Minute, slog.New(slog.NewJSONHandler(&logs, nil)))
	if _, err := client.Validate(context.Background(), "very-secret-access-token", "corr"); err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	hit, err := client.Validate(context.Background(), "very-secret-access-token", "corr")
	if err != nil || hit.ClientID != "media" {
		t.Fatalf("cache hit failed: %v", err)
	}
	if strings.Contains(logs.String(), "very-secret-access-token") {
		t.Fatal("cache hit log leaked the access token")
	}
	if !strings.Contains(logs.String(), `"cache_hit":true`) {
		t.Fatal("cache hit log missing cache_hit marker")
	}
}
