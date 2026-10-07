// Package httpclient configures outbound HTTP connections within Client API.
package httpclient

import (
	"net/http"
	"time"
)

// New retains a bounded idle pool for the default 64 admitted requests instead
// of the standard transport's two idle connections per dependency. Auth still
// validates every request; only TCP connections are reused.
func New(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 64
	transport.MaxIdleConnsPerHost = 64
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
