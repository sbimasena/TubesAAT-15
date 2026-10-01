package aggregator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const provisionalPath = "/internal/v1/hazards"
const maxResponseBytes = 10 << 20

type ErrorKind string

const (
	ErrorCanceled        ErrorKind = "canceled"
	ErrorTimeout         ErrorKind = "timeout"
	ErrorUpstreamStatus  ErrorKind = "upstream_status"
	ErrorInvalidResponse ErrorKind = "invalid_response"
	ErrorTransport       ErrorKind = "transport"
)

type RequestError struct {
	Kind   ErrorKind
	Status int
	Err    error
}

func (e *RequestError) Error() string { return fmt.Sprintf("aggregator %s: %v", e.Kind, e.Err) }
func (e *RequestError) Unwrap() error { return e.Err }

type Client struct {
	base   *url.URL
	http   *http.Client
	logger *slog.Logger
}

func NewClient(baseURL string, timeout time.Duration, logger *slog.Logger) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid Aggregator base URL")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("Aggregator timeout must be positive")
	}
	return &Client{base: base, http: &http.Client{Timeout: timeout}, logger: logger}, nil
}

// List forwards only the four filters supported by the observed Aggregator API.
// NOT FINAL: the API contract awaits Member A's agreement; canonical mapping remains in Aggregator.
func (c *Client) List(ctx context.Context, filters url.Values, correlationID string) ([]byte, error) {
	endpoint := c.base.ResolveReference(&url.URL{Path: provisionalPath})
	query := url.Values{}
	for _, key := range []string{"source", "hazard_type", "since", "limit"} {
		if value := filters.Get(key); value != "" {
			query.Set(key, value)
		}
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, &RequestError{Kind: ErrorTransport, Err: err}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Correlation-ID", correlationID)
	started := time.Now()
	response, err := c.http.Do(request)
	if err != nil {
		kind := classifyError(err)
		c.logCall(correlationID, started, 0, kind)
		return nil, &RequestError{Kind: kind, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		c.logCall(correlationID, started, response.StatusCode, ErrorUpstreamStatus)
		return nil, &RequestError{Kind: ErrorUpstreamStatus, Status: response.StatusCode,
			Err: fmt.Errorf("HTTP %d", response.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		kind := classifyError(err)
		c.logCall(correlationID, started, response.StatusCode, kind)
		return nil, &RequestError{Kind: kind, Status: response.StatusCode, Err: err}
	}
	if len(data) > maxResponseBytes || !validEnvelope(data) {
		c.logCall(correlationID, started, response.StatusCode, ErrorInvalidResponse)
		return nil, &RequestError{Kind: ErrorInvalidResponse, Status: response.StatusCode,
			Err: fmt.Errorf("invalid or oversized response")}
	}
	c.logCall(correlationID, started, response.StatusCode, "")
	return data, nil
}

func classifyError(err error) ErrorKind {
	if errors.Is(err, context.Canceled) {
		return ErrorCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorTimeout
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ErrorTimeout
	}
	return ErrorTransport
}

func validEnvelope(data []byte) bool {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return false
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(envelope["data"], &rows); err != nil || rows == nil {
		return false
	}
	var count int
	if err := json.Unmarshal(envelope["count"], &count); err != nil || count < 0 {
		return false
	}
	var sources map[string]json.RawMessage
	return json.Unmarshal(envelope["sources"], &sources) == nil && sources != nil
}

func (c *Client) logCall(id string, started time.Time, status int, kind ErrorKind) {
	if c.logger == nil {
		return
	}
	attrs := []any{"service", "client-api", "target", "aggregator", "operation", provisionalPath,
		"correlation_id", id, "latency_ms", float64(time.Since(started).Microseconds()) / 1000,
		"status", status}
	if kind != "" {
		attrs = append(attrs, "error_kind", strings.ReplaceAll(string(kind), "_", "-"))
		c.logger.Warn("outbound request failed", attrs...)
		return
	}
	c.logger.Info("outbound request complete", attrs...)
}
