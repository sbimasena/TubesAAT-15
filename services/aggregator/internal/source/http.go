package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

type BMKGClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
	logger  *slog.Logger
}

type PVMBGClient struct {
	baseURL string
	token   string
	client  *http.Client
	logger  *slog.Logger
}

func NewBMKGClient(baseURL, apiKey string, logger *slog.Logger) *BMKGClient {
	return &BMKGClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 2 * time.Second},
		logger:  logger,
	}
}

func NewPVMBGClient(baseURL, token string, logger *slog.Logger) *PVMBGClient {
	return &PVMBGClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client:  &http.Client{Timeout: 4 * time.Second},
		logger:  logger,
	}
}

func (c *BMKGClient) FetchSeismicEvents(ctx context.Context, since time.Time, correlationID string) ([]domain.SeismicEvent, error) {
	var events []domain.SeismicEvent
	if err := c.getJSON(ctx, "/seismic-events", since, correlationID, "X-BMKG-Key", c.apiKey, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (c *BMKGClient) FetchTsunamiWarnings(ctx context.Context, since time.Time, correlationID string) ([]domain.TsunamiWarning, error) {
	var warnings []domain.TsunamiWarning
	if err := c.getJSON(ctx, "/tsunami-warnings", since, correlationID, "X-BMKG-Key", c.apiKey, &warnings); err != nil {
		return nil, err
	}
	return warnings, nil
}

func (c *PVMBGClient) FetchVolcanicReports(ctx context.Context, since time.Time, correlationID string) ([]domain.VolcanicReport, error) {
	var reports []domain.VolcanicReport
	if err := c.getJSON(ctx, "/volcanic-reports", since, correlationID, "Authorization", "Bearer "+c.token, &reports); err != nil {
		return nil, err
	}
	return reports, nil
}

type jsonGetter struct {
	baseURL string
	client  *http.Client
	logger  *slog.Logger
	target  string
}

func (c *BMKGClient) getJSON(ctx context.Context, path string, since time.Time, correlationID, authHeader, credential string, destination any) error {
	return jsonGetter{c.baseURL, c.client, c.logger, "bmkg"}.get(ctx, path, since, correlationID, authHeader, credential, destination)
}

func (c *PVMBGClient) getJSON(ctx context.Context, path string, since time.Time, correlationID, authHeader, credential string, destination any) error {
	return jsonGetter{c.baseURL, c.client, c.logger, "pvmbg"}.get(ctx, path, since, correlationID, authHeader, credential, destination)
}

func (g jsonGetter) get(ctx context.Context, path string, since time.Time, correlationID, authHeader, credential string, destination any) error {
	endpoint, err := url.Parse(g.baseURL + path)
	if err != nil {
		return fmt.Errorf("parse %s URL: %w", g.target, err)
	}
	if !since.IsZero() {
		query := endpoint.Query()
		query.Set("since", since.UTC().Format(time.RFC3339Nano))
		endpoint.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create %s request: %w", g.target, err)
	}
	request.Header.Set(authHeader, credential)
	request.Header.Set("X-Correlation-ID", correlationID)
	request.Header.Set("Accept", "application/json")

	started := time.Now()
	response, err := g.client.Do(request)
	if err != nil {
		g.logCall(correlationID, path, time.Since(started), 0, err)
		return fmt.Errorf("call %s %s: %w", g.target, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		err := fmt.Errorf("upstream returned %s: %s", response.Status, strings.TrimSpace(string(body)))
		g.logCall(correlationID, path, time.Since(started), response.StatusCode, err)
		return fmt.Errorf("call %s %s: %w", g.target, path, err)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 10<<20))
	if err := decoder.Decode(destination); err != nil {
		g.logCall(correlationID, path, time.Since(started), response.StatusCode, err)
		return fmt.Errorf("decode %s %s response: %w", g.target, path, err)
	}
	g.logCall(correlationID, path, time.Since(started), response.StatusCode, nil)
	return nil
}

func (g jsonGetter) logCall(correlationID, operation string, duration time.Duration, status int, err error) {
	if g.logger == nil {
		return
	}
	attrs := []any{
		"service", "aggregator",
		"target", g.target,
		"operation", operation,
		"correlation_id", correlationID,
		"latency_ms", float64(duration.Microseconds()) / 1000,
		"status", status,
	}
	if err != nil {
		attrs = append(attrs, "error", err)
		g.logger.Warn("upstream request failed", attrs...)
		return
	}
	g.logger.Info("upstream request complete", attrs...)
}
