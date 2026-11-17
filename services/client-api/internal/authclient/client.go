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
	"sync"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/httpclient"
)

var (
	ErrInvalid     = errors.New("invalid access token")
	ErrUnavailable = errors.New("Auth unavailable")
)

// MaxCachedTokens bounds the introspection cache. Entries are tokens; the map
// is cleared when it grows past this point, which is safe (the next request
// simply introspects again).
const MaxCachedTokens = 4096

type Identity struct {
	ClientID  string `json:"client_id"`
	Scope     string `json:"scope"`
	SessionID string `json:"session_id"`
}

type cacheEntry struct {
	identity Identity
	expires  time.Time
}

type Client struct {
	endpoint string
	secret   string
	http     *http.Client
	logger   *slog.Logger
	// cacheTTL of 0 disables caching (M1 semantics: every request asks Auth, so
	// a token becomes invalid immediately after refresh). Enable it for load
	// tests and game day via AUTH_INTROSPECTION_CACHE_TTL_MS; the documented
	// trade-off is that a token may stay accepted up to cacheTTL after refresh.
	cacheTTL time.Duration
	cacheMu  sync.Mutex
	cache    map[string]cacheEntry
}

func New(baseURL, secret string, timeout time.Duration, cacheTTL time.Duration, logger *slog.Logger) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil ||
		base.Path != "" || base.RawQuery != "" || base.Fragment != "" || len(secret) < 32 || timeout <= 0 || cacheTTL < 0 {
		return nil, fmt.Errorf("Auth requires an HTTP(S) origin, internal secret of at least 32 bytes, and positive timeout")
	}
	return &Client{endpoint: base.ResolveReference(&url.URL{Path: "/internal/v1/introspect"}).String(),
		secret: secret, logger: logger, http: httpclient.New(timeout), cacheTTL: cacheTTL,
		cache: make(map[string]cacheEntry)}, nil
}

func (c *Client) lookupCache(accessToken string, now time.Time) (Identity, bool) {
	if c.cacheTTL <= 0 {
		return Identity{}, false
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	entry, ok := c.cache[accessToken]
	if !ok || !now.Before(entry.expires) {
		if ok {
			delete(c.cache, accessToken)
		}
		return Identity{}, false
	}
	return entry.identity, true
}

func (c *Client) storeCache(accessToken string, identity Identity, now time.Time) {
	if c.cacheTTL <= 0 {
		return
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if len(c.cache) >= MaxCachedTokens {
		for key, entry := range c.cache {
			if !now.Before(entry.expires) {
				delete(c.cache, key)
			}
		}
	}
	if len(c.cache) >= MaxCachedTokens {
		c.cache = make(map[string]cacheEntry)
	}
	c.cache[accessToken] = cacheEntry{identity: identity, expires: now.Add(c.cacheTTL)}
}

// Validate asks Auth whether the token is active; successful identities are
// cached for cacheTTL when caching is enabled. Rejected tokens and Auth
// failures are never cached, so revoked tokens fail as soon as the cache
// entry for their last successful validation expires.
func (c *Client) Validate(ctx context.Context, accessToken, correlationID string) (identity Identity, err error) {
	if hit, ok := c.lookupCache(accessToken, time.Now()); ok {
		if c.logger != nil {
			c.logger.Info("introspection cache hit", "service", "client-api", "target", "auth",
				"operation", "/internal/v1/introspect", "correlation_id", correlationID,
				"client_id", hit.ClientID, "scope", hit.Scope, "cache_hit", true)
		}
		return hit, nil
	}
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
	c.storeCache(accessToken, payload.Identity, time.Now())
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
