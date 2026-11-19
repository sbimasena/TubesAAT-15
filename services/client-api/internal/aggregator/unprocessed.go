package aggregator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SetAuditToken configures the service credential before the client is shared.
func (c *Client) SetAuditToken(token string) { c.auditToken = strings.TrimSpace(token) }

type UnprocessedItem struct {
	MessageID  string    `json:"message_id"`
	Source     string    `json:"source"`
	ReceivedAt time.Time `json:"received_at"`
	Reason     string    `json:"reason"`
	Retryable  bool      `json:"retryable"`
	Attempts   int       `json:"attempts"`
	Status     string    `json:"status"`
}

type UnprocessedPage struct {
	Items      []UnprocessedItem `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

func (c *Client) Unprocessed(ctx context.Context, query url.Values, id string) (UnprocessedPage, error) {
	var page UnprocessedPage
	if c.auditToken == "" {
		return page, &RequestError{Kind: ErrorUpstreamStatus, Status: http.StatusServiceUnavailable, Err: errors.New("audit credential not configured")}
	}
	endpoint := c.base.ResolveReference(&url.URL{Path: "/internal/audit/unprocessed", RawQuery: query.Encode()})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return page, &RequestError{Kind: ErrorTransport, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+c.auditToken)
	req.Header.Set("X-Correlation-ID", id)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return page, &RequestError{Kind: classifyError(err), Err: err}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return page, &RequestError{Kind: ErrorUpstreamStatus, Status: res.StatusCode, Err: fmt.Errorf("HTTP %d", res.StatusCode)}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return page, &RequestError{Kind: classifyError(err), Err: err}
	}
	if len(raw) > maxResponseBytes || json.Unmarshal(raw, &page) != nil || page.Items == nil {
		return UnprocessedPage{}, &RequestError{Kind: ErrorInvalidResponse, Err: errors.New("invalid audit response")}
	}
	return page, nil
}

func (c *Client) ReplayPVMBG(ctx context.Context, body []byte, id string) error {
	if c.auditToken == "" {
		return &RequestError{Kind: ErrorUpstreamStatus, Status: http.StatusServiceUnavailable, Err: errors.New("audit credential not configured")}
	}
	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()
	endpoint := c.base.ResolveReference(&url.URL{Path: "/internal/audit/unprocessed/replay"})
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
		if err != nil {
			return &RequestError{Kind: ErrorTransport, Err: err}
		}
		req.Close = true
		req.Header.Set("Authorization", "Bearer "+c.auditToken)
		req.Header.Set("X-Correlation-ID", id)
		req.Header.Set("Content-Type", "application/json")
		res, err := c.http.Do(req)
		if err != nil {
			return &RequestError{Kind: classifyError(err), Err: err}
		}
		status := res.StatusCode
		follower := status == http.StatusServiceUnavailable && res.Header.Get("Retry-After") == "1"
		res.Body.Close()
		if status == http.StatusAccepted {
			return nil
		}
		// Only the explicit follower response means no write was attempted.
		if !follower || attempt == 4 {
			return &RequestError{Kind: ErrorUpstreamStatus, Status: status, Err: fmt.Errorf("HTTP %d", status)}
		}
	}
}
