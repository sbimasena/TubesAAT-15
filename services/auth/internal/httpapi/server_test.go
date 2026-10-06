package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/auth/internal/identity"
	"github.com/sbimasena/TubesAAT-15/services/auth/internal/session"
	"github.com/sbimasena/TubesAAT-15/services/auth/internal/token"
)

func TestHealthAndCorrelationID(t *testing.T) {
	handler := NewHandler(nil, nil, "")
	for _, incoming := range []string{"", "request-123"} {
		request := httptest.NewRequest(http.MethodGet, "/health", nil)
		if incoming != "" {
			request.Header.Set("X-Correlation-ID", incoming)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("health returned %d", response.Code)
		}
		id := response.Header().Get("X-Correlation-ID")
		if id == "" || (incoming != "" && id != incoming) {
			t.Fatalf("unexpected correlation ID %q for input %q", id, incoming)
		}
		var body map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["service"] != "auth" || body["status"] != "ok" {
			t.Fatalf("unexpected health response: %s (%v)", response.Body.String(), err)
		}
	}
}

func TestLoginRefreshAndProtectedIntrospection(t *testing.T) {
	env := map[string]string{
		"MEDIA_CLIENT_ID": "media", "MEDIA_CLIENT_PASSWORD": "test-media-password",
		"FIELD_TEAM_CLIENT_ID": "field-team", "FIELD_TEAM_CLIENT_PASSWORD": "test-field-password",
		"INTERNAL_OPS_CLIENT_ID": "internal-ops", "INTERNAL_OPS_CLIENT_PASSWORD": "test-ops-password",
	}
	identities, err := identity.NewFromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	issuer, _ := token.New([]byte(strings.Repeat("jwt-test", 4)), time.Minute)
	sessions := session.New(identities, issuer, time.Minute, time.Hour)
	secret := strings.Repeat("internal-test", 3)
	var logs bytes.Buffer
	sensitiveValues := []string{"test-media-password", "test-field-password", "test-ops-password", secret,
		strings.Repeat("jwt-test", 4), "access_token", "refresh_token"}
	handler := NewHandler(slog.New(slog.NewJSONHandler(&logs, nil)), sessions, secret)
	call := func(path, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("X-Correlation-ID", "auth-test")
		if key != "" {
			r.Header.Set("X-Auth-Service-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Correlation-ID") != "auth-test" {
			t.Fatal("cache/correlation headers missing")
		}
		return w
	}
	for _, test := range []struct{ id, password string }{
		{"media", "test-media-password"}, {"field-team", "test-field-password"}, {"internal-ops", "test-ops-password"},
	} {
		body, _ := json.Marshal(map[string]string{"client_id": test.id, "password": test.password})
		w := call("/login", string(body), "")
		var old session.Pair
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &old) != nil || old.TokenType != "Bearer" || old.ExpiresIn != 60 {
			t.Fatal("valid login failed")
		}
		var publicPair map[string]json.RawMessage
		if json.Unmarshal(w.Body.Bytes(), &publicPair) != nil || len(publicPair) != 4 || old.ClientID != "" || old.Scope != "" {
			t.Fatal("logging metadata changed the token response contract")
		}
		sensitiveValues = append(sensitiveValues, old.AccessToken, old.RefreshToken)
		claims, err := sessions.Inspect(context.Background(), old.AccessToken)
		if err != nil || claims.Scope != test.id {
			t.Fatal("login did not map identity to scope")
		}
		input, _ := json.Marshal(map[string]string{"token": old.AccessToken})
		if call("/internal/v1/introspect", string(input), "").Code != 401 || call("/internal/v1/introspect", string(input), "wrong").Code != 401 {
			t.Fatal("introspection accepts missing/wrong service credential")
		}
		if call("/internal/v1/introspect", string(input), secret).Code != 200 {
			t.Fatal("valid introspection failed")
		}
		refresh, _ := json.Marshal(map[string]string{"refresh_token": old.RefreshToken})
		w = call("/refresh", string(refresh), "")
		var next session.Pair
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &next) != nil || next.AccessToken == old.AccessToken {
			t.Fatal("refresh failed")
		}
		sensitiveValues = append(sensitiveValues, next.AccessToken, next.RefreshToken)
		w = call("/internal/v1/introspect", string(input), secret)
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"active":false}` {
			t.Fatal("old access token remains active")
		}
		if call("/refresh", string(refresh), "").Code != 401 {
			t.Fatal("old refresh token replay accepted")
		}
	}
	for _, body := range []string{`{"client_id":"media","password":"test-field-password"}`, `{"client_id":"unknown","password":"test-media-password"}`} {
		if call("/login", body, "").Code != 401 {
			t.Fatal("cross/unknown credentials accepted")
		}
	}
	for _, body := range []string{`{"client_id":"media","password":"test-media-password","scope":"internal-ops"}`, `{}`, `{} {}`, `not-json`, strings.Repeat("x", 8193)} {
		status := call("/login", body, "").Code
		if status != 400 && status != 401 {
			t.Fatal("invalid input accepted")
		}
	}
	for _, sensitive := range sensitiveValues {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatal("Auth logs contain sensitive data")
		}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			t.Fatal("invalid JSON log")
		}
		if record["msg"] != "request complete" {
			continue
		}
		if record["time"] == nil || record["level"] == nil || record["service"] != "auth" ||
			record["correlation_id"] != "auth-test" || record["latency_ms"] == nil {
			t.Fatal("request log missing observability fields")
		}
		operation, _ := record["operation"].(string)
		if operation != "/login" && operation != "/refresh" {
			continue
		}
		if record["status"] != float64(200) {
			if record["client_id"] != nil || record["scope"] != nil {
				t.Fatal("failed request logged an unverified identity")
			}
			continue
		}
		id, _ := record["client_id"].(string)
		if id == "" || record["scope"] != id {
			t.Fatal("login/refresh lacks verified identity/scope")
		}
		seen[operation+"/"+id] = true
	}
	if len(seen) != 6 {
		t.Fatal("login/refresh traces incomplete for the three roles")
	}
}
