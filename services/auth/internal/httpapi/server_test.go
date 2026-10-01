package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthAndCorrelationID(t *testing.T) {
	handler := NewHandler(nil)
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
