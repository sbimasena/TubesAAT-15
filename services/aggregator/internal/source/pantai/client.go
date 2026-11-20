// Package pantai implements the public Sensor Pantai HTTP contract. It does
// not infer coastal severity or own persistence, cursor advancement or retries.
package pantai

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const responseLimit = 10 << 20
const captureLimit = 32 << 20

type Config struct {
	BaseURL    string
	Token      string
	Timeout    time.Duration
	InstanceID string
	Logger     *slog.Logger
	// Verify must check the active leader term before each upstream request.
	Verify func(context.Context) error
}

type Client struct {
	config Config
	http   *http.Client
}

// New uses a separate connection pool and never follows redirects with tokens.
func New(config Config) (*Client, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("pantai: base URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	if strings.TrimSpace(config.Token) == "" || strings.ContainsAny(config.Token, "\r\n") {
		return nil, fmt.Errorf("pantai: a nonempty bearer token is required")
	}
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Second
	}
	if config.Timeout < 0 {
		return nil, fmt.Errorf("pantai: timeout must be positive")
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 2
	transport.MaxConnsPerHost = 2
	transport.ResponseHeaderTimeout = config.Timeout
	return &Client{config: config, http: &http.Client{
		Transport: transport, Timeout: config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

// HTTPError carries status and Retry-After without retaining an upstream body.
type HTTPError struct {
	StatusCode int
	RetryAfter string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("pantai: upstream HTTP %d", e.StatusCode) }

func (c *Client) get(ctx context.Context, path string, query url.Values, correlationID string) (raw []byte, truncated bool, err error) {
	if c.config.Verify != nil {
		if err := c.config.Verify(ctx); err != nil {
			return nil, false, err
		}
	}
	endpoint := c.config.BaseURL + path
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Authorization", "Bearer "+c.config.Token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Correlation-ID", correlationID)
	request.Header.Set("X-Instance-ID", c.config.InstanceID)
	started, status := time.Now(), 0
	defer func() {
		if c.config.Logger != nil {
			c.config.Logger.Info("outbound request", "target", "sensor-pantai", "path", path,
				"correlation_id", correlationID, "status", status, "latency_ms", time.Since(started).Milliseconds(),
				"failed", err != nil, "truncated", truncated)
		}
	}()
	response, err := c.http.Do(request)
	if err != nil {
		return nil, false, fmt.Errorf("pantai request: %w", err)
	}
	defer response.Body.Close()
	status = response.StatusCode
	if status != http.StatusOK {
		return nil, false, &HTTPError{StatusCode: status, RetryAfter: response.Header.Get("Retry-After")}
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, captureLimit+1))
	if err != nil {
		return nil, false, fmt.Errorf("pantai response body: %w", err)
	}
	if len(raw) > captureLimit {
		// A partial response must never be archived as a complete DLQ message.
		return nil, true, nil
	}
	return raw, false, nil
}

func (c *Client) Stations(ctx context.Context, correlationID string) ([]Station, error) {
	raw, truncated, err := c.get(ctx, "/v1/stations", nil, correlationID)
	if err != nil {
		return nil, err
	}
	if truncated || len(raw) > responseLimit {
		return nil, violation("stations_too_large")
	}
	return decodeStations(raw)
}

// Observations returns the server's cursor verbatim. Only a durable worker may
// advance a checkpoint; HTTP success alone never authorizes that advancement.
func (c *Client) Observations(ctx context.Context, cursor string, limit int, correlationID string) (Batch, error) {
	if limit <= 0 || limit > 1000 {
		return Batch{}, fmt.Errorf("pantai: limit must be between 1 and 1000")
	}
	query := url.Values{"limit": {fmt.Sprint(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	raw, truncated, err := c.get(ctx, "/v1/observations", query, correlationID)
	if err != nil {
		return Batch{}, err
	}
	if truncated {
		return Batch{Truncated: true, Invalid: violation("response_capture_limit")}, nil
	}
	return DecodeBatch(raw), nil
}
