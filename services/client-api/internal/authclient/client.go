package authclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid access token")
	ErrUnavailable = errors.New("Auth unavailable")
)

type Identity struct {
	ClientID  string `json:"client_id"`
	Scope     string `json:"scope"`
	SessionID string `json:"session_id"`
}

type Client struct {
	endpoint string
	secret   string
	http     *http.Client
	logger   *slog.Logger
}

func New(baseURL, secret string, timeout time.Duration, logger *slog.Logger) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil ||
		base.Path != "" || base.RawQuery != "" || base.Fragment != "" || len(secret) < 32 || timeout <= 0 {
		return nil, fmt.Errorf("Auth requires an HTTP(S) origin, internal secret of at least 32 bytes, and positive timeout")
	}
	return &Client{endpoint: base.ResolveReference(&url.URL{Path: "/internal/v1/introspect"}).String(),
		secret: secret, logger: logger, http: &http.Client{Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Validate always asks Auth; caching would delay rejection after a refresh.
func (c *Client) Validate(ctx context.Context, accessToken, correlationID string) (identity Identity, err error) {
	started := time.Now()
	status := 0
	errorKind := ""
	defer func() {
		if c.logger != nil {
			attrs := []any{"service", "client-api", "target", "auth",
				"operation", "/internal/v1/introspect", "correlation_id", correlationID,
				"latency_ms", float64(time.Since(started).Microseconds()) / 1000, "status", status}
			if errorKind != "" {
				attrs = append(attrs, "error_kind", errorKind)
			}
			if identity.ClientID != "" {
				attrs = append(attrs, "client_id", identity.ClientID, "scope", identity.Scope)
			}
			if errors.Is(err, ErrUnavailable) {
				c.logger.Warn("outbound request failed", attrs...)
			} else {
				c.logger.Info("outbound request complete", attrs...)
			}
		}
	}()
	body, _ := json.Marshal(map[string]string{"token": accessToken})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		errorKind = "transport"
		return Identity{}, ErrUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Auth-Service-Key", c.secret)
	request.Header.Set("X-Correlation-ID", correlationID)
	response, err := c.http.Do(request)
	if err != nil {
		errorKind = classifyFailure(err)
		return Identity{}, ErrUnavailable
	}
	defer response.Body.Close()
	status = response.StatusCode
	if status != http.StatusOK {
		errorKind = "upstream_status"
		// Internal credential failure is a service configuration error, not a bad user token.
		return Identity{}, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil {
		errorKind = classifyFailure(err)
		return Identity{}, ErrUnavailable
	}
	if len(data) > 8192 {
		errorKind = "invalid_response"
		return Identity{}, ErrUnavailable
	}
	var payload struct {
		Active *bool `json:"active"`
		Identity
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		errorKind = "invalid_response"
		return Identity{}, ErrUnavailable
	}
	if payload.Active == nil {
		errorKind = "invalid_response"
		return Identity{}, ErrUnavailable
	}
	if !*payload.Active {
		errorKind = "invalid_token"
		return Identity{}, ErrInvalid
	}
	if payload.ClientID == "" || payload.SessionID == "" ||
		(payload.Scope != "media" && payload.Scope != "field-team" && payload.Scope != "internal-ops") {
		errorKind = "invalid_response"
		return Identity{}, ErrUnavailable
	}
	return payload.Identity, nil
}

func classifyFailure(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	return "transport"
}
