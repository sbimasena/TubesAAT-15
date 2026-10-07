package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckHealth(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				t.Errorf("unexpected health path: %s", r.URL.Path)
			}
			w.WriteHeader(status)
		}))
		err := checkHealth(server.URL + "/health")
		server.Close()
		if (err == nil) != (status == http.StatusOK) {
			t.Fatalf("status %d: unexpected health result %v", status, err)
		}
		if err := checkHealth(server.URL + "/health"); err == nil {
			t.Fatal("health check accepted an unreachable service")
		}
	}
}
