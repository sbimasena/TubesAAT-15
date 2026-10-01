package aggregator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

const validResponse = `{"data":[],"count":0,"sources":{"BMKG":{"available":true}}}`

func TestListForwardsSupportedFiltersAndCorrelationID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != provisionalPath || r.URL.Query().Get("source") != "BMKG" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("unexpected request URL: %s", r.URL.String())
		}
		if r.URL.Query().Has("unsupported") {
			t.Error("unsupported filter was forwarded")
		}
		if r.Header.Get("X-Correlation-ID") != "request-123" {
			t.Errorf("correlation ID not forwarded")
		}
		_, _ = w.Write([]byte(validResponse))
	}))
	defer upstream.Close()
	client, err := NewClient(upstream.URL, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.List(context.Background(), url.Values{"source": {"BMKG"}, "limit": {"5"}, "unsupported": {"x"}}, "request-123")
	if err != nil || string(response) != validResponse {
		t.Fatalf("unexpected result: %s (%v)", response, err)
	}
}

func TestListClassifiesTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client, _ := NewClient(upstream.URL, 20*time.Millisecond, nil)
	_, err := client.List(context.Background(), nil, "timeout-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorTimeout {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestListRejectsMalformedResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":0}`))
	}))
	defer upstream.Close()
	client, _ := NewClient(upstream.URL, time.Second, nil)
	_, err := client.List(context.Background(), nil, "malformed-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorInvalidResponse {
		t.Fatalf("expected invalid response, got %v", err)
	}
}

func TestListClassifiesUpstreamStatus(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		client, _ := NewClient(upstream.URL, time.Second, nil)
		_, err := client.List(context.Background(), nil, "status-test")
		upstream.Close()
		var requestErr *RequestError
		if !errors.As(err, &requestErr) || requestErr.Kind != ErrorUpstreamStatus || requestErr.Status != status {
			t.Fatalf("status %d: unexpected error %v", status, err)
		}
	}
}

func TestListHonorsCanceledContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(validResponse))
	}))
	defer upstream.Close()
	client, _ := NewClient(upstream.URL, time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.List(ctx, nil, "canceled-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorCanceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestListClassifiesConnectionRefused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	upstream.Close()
	client, err := NewClient(upstream.URL, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(context.Background(), nil, "connection-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorTransport {
		t.Fatalf("expected transport failure, got %v", err)
	}
}

func TestListClassifiesResponseBodyTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client, err := NewClient(upstream.URL, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(context.Background(), nil, "body-timeout-test")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Kind != ErrorTimeout {
		t.Fatalf("expected body timeout, got %v", err)
	}
}
