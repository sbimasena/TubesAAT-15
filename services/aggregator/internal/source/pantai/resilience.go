package pantai

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type Upstream interface {
	Stations(context.Context, string) ([]Station, error)
	Observations(context.Context, string, int, string) (Batch, error)
}

var ErrDataHealth = errors.New("pantai: data health check failed")

type ResilienceConfig struct {
	FailureThreshold int
	OpenFor          time.Duration
	MaxAttempts      int
	RetryBase        time.Duration
	RetryMax         time.Duration
	RetryWindow      time.Duration
	RetriesPerWindow int
	// CheckPage must be a pure data-health check, not a persistence operation.
	// A future worker supplies its virtual-time freshness policy here. A nil
	// check does not establish freshness merely because HTTP returned 200.
	CheckPage func(Batch) error
}

// DefaultResilienceConfig is provisional tuning, not measured P5 capacity.
func DefaultResilienceConfig() ResilienceConfig {
	return ResilienceConfig{FailureThreshold: 3, OpenFor: 10 * time.Second, MaxAttempts: 2,
		RetryBase: 250 * time.Millisecond, RetryMax: 2 * time.Second,
		RetryWindow: 10 * time.Second, RetriesPerWindow: 2}
}

type ResilienceStatus struct {
	State               string    `json:"state"`
	InFlight            bool      `json:"in_flight"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	RetryAt             time.Time `json:"retry_at,omitempty"`
	LastSuccess         time.Time `json:"last_success,omitempty"`
	LastFailure         time.Time `json:"last_failure,omitempty"`
	Attempts            uint64    `json:"attempts"`
	Retries             uint64    `json:"retries"`
	Rejected            uint64    `json:"rejected"`
}

// GateError is a fast rejection. A worker may schedule the next attempt from
// RetryAt without blocking an API read or creating more upstream requests.
type GateError struct {
	Reason  string
	RetryAt time.Time
}

func (e *GateError) Error() string { return "pantai: " + e.Reason }

// Resilient serializes calls to one source, shares its retry budget across
// endpoints, and allows only one half-open probe. It owns no database or cache.
type Resilient struct {
	upstream  Upstream
	config    ResilienceConfig
	mu        sync.Mutex
	status    ResilienceStatus
	notBefore time.Time
	window    time.Time
	remaining int
	now       func() time.Time
	wait      func(context.Context, time.Duration) error
	jitter    func(time.Duration) time.Duration
}

// Validate checks the resilience settings before a leader starts its worker.
func (c ResilienceConfig) Validate() error {
	if c.FailureThreshold < 1 || c.OpenFor <= 0 || c.MaxAttempts < 1 || c.MaxAttempts > 10 ||
		c.RetryBase <= 0 || c.RetryMax < c.RetryBase || c.RetryWindow <= 0 || c.RetriesPerWindow < 0 {
		return fmt.Errorf("pantai: invalid resilience configuration")
	}
	return nil
}

func NewResilient(upstream Upstream, config ResilienceConfig) (*Resilient, error) {
	if upstream == nil {
		return nil, fmt.Errorf("pantai: missing upstream")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Resilient{upstream: upstream, config: config, status: ResilienceStatus{State: "closed"},
		now: time.Now, wait: waitContext, jitter: func(delay time.Duration) time.Duration {
			// Equal jitter keeps a positive floor; it cannot produce a hot loop.
			half := delay/2 + delay%2
			return half + time.Duration(rand.Int64N(int64(delay-half+1)))
		}}, nil
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Resilient) Status() ResilienceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.status
	status.RetryAt = later(status.RetryAt, r.notBefore)
	return status
}

func (r *Resilient) admit(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.status.InFlight {
		r.status.Rejected++
		return false, &GateError{Reason: "bulkhead_full", RetryAt: r.status.RetryAt}
	}
	if now.Before(r.status.RetryAt) {
		r.status.Rejected++
		return false, &GateError{Reason: "circuit_open", RetryAt: r.status.RetryAt}
	}
	if now.Before(r.notBefore) {
		r.status.Rejected++
		return false, &GateError{Reason: "backoff", RetryAt: r.notBefore}
	}
	probe := r.status.State == "open"
	if probe {
		r.status.State = "half_open"
	}
	r.status.InFlight = true
	return probe, nil
}

func (r *Resilient) finish(ctx context.Context, err error, probe bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.InFlight = false
	now := r.now()
	if ctx.Err() != nil || errors.Is(err, ErrLeaseVerification) {
		// Losing the caller/leader does not prove the source failed or recovered.
		if probe {
			r.status.State = "open"
			r.status.RetryAt = now.Add(r.config.OpenFor)
		}
		return
	}
	if err == nil {
		r.status.State = "closed"
		r.status.ConsecutiveFailures = 0
		r.status.RetryAt = time.Time{}
		r.status.LastSuccess = now
		r.notBefore = time.Time{}
		return
	}
	r.status.ConsecutiveFailures++
	r.status.LastFailure = now
	if probe || r.status.ConsecutiveFailures >= r.config.FailureThreshold {
		r.status.State = "open"
		r.status.RetryAt = now.Add(r.config.OpenFor)
	}
	// Also pace failed calls below the threshold, even when the retry budget
	// is exhausted or the outer poller calls again immediately.
	r.notBefore = later(r.notBefore, now.Add(r.delay(r.status.ConsecutiveFailures-1)))
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (r *Resilient) delay(attempt int) time.Duration {
	delay := r.config.RetryBase
	for i := 0; i < attempt && delay < r.config.RetryMax; i++ {
		if delay > r.config.RetryMax/2 {
			return r.jitter(r.config.RetryMax)
		}
		delay *= 2
	}
	return r.jitter(min(delay, r.config.RetryMax))
}

func (r *Resilient) reserveRetry() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.window.IsZero() || now.Sub(r.window) >= r.config.RetryWindow {
		r.window, r.remaining = now, r.config.RetriesPerWindow
	}
	if r.remaining == 0 {
		return false
	}
	r.remaining--
	return true
}

func retryable(err error) bool {
	if errors.Is(err, ErrLeaseVerification) || errors.Is(err, ErrDataHealth) || errors.Is(err, context.Canceled) {
		return false
	}
	var upstream *HTTPError
	if errors.As(err, &upstream) {
		return upstream.StatusCode == 408 || upstream.StatusCode == 429 || upstream.StatusCode >= 500 && upstream.StatusCode <= 599
	}
	var network net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network)
}

func retryAfter(err error, now time.Time) time.Time {
	var upstream *HTTPError
	if !errors.As(err, &upstream) {
		return time.Time{}
	}
	if seconds, parseErr := strconv.ParseInt(upstream.RetryAfter, 10, 64); parseErr == nil && seconds >= 0 {
		// Saturate safely instead of overflowing into a negative delay.
		seconds = min(seconds, int64((1<<63-1)/int64(time.Second)))
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if stamp, err := http.ParseTime(upstream.RetryAfter); err == nil && stamp.After(now) {
		return stamp
	}
	return time.Time{}
}

func (r *Resilient) execute(ctx context.Context, call func() error) (err error) {
	probe, err := r.admit(ctx)
	if err != nil {
		return err
	}
	defer func() { r.finish(ctx, err, probe) }()
	maxAttempts := r.config.MaxAttempts
	if probe {
		maxAttempts = 1
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		r.mu.Lock()
		r.status.Attempts++
		if attempt > 0 {
			r.status.Retries++
		}
		r.mu.Unlock()
		err = call()
		if err == nil || ctx.Err() != nil || !retryable(err) {
			return err
		}
		now := r.now()
		hint := retryAfter(err, now)
		r.mu.Lock()
		r.notBefore = later(r.notBefore, hint)
		r.mu.Unlock()
		delay := r.delay(attempt)
		if hint.After(now) {
			delay = max(delay, hint.Sub(now))
		}
		// A long Retry-After is a scheduling hint, not permission to hold the
		// caller indefinitely or to retry earlier than requested by the server.
		if attempt+1 == maxAttempts || delay > r.config.RetryMax || !r.reserveRetry() {
			return err
		}
		if err = r.wait(ctx, delay); err != nil {
			return err
		}
	}
	return err
}

func (r *Resilient) Stations(ctx context.Context, correlationID string) (stations []Station, err error) {
	err = r.execute(ctx, func() error {
		var err error
		stations, err = r.upstream.Stations(ctx, correlationID)
		return err
	})
	return stations, err
}

func (r *Resilient) Observations(ctx context.Context, cursor string, limit int, correlationID string) (batch Batch, err error) {
	if limit <= 0 || limit > 1000 {
		return batch, fmt.Errorf("pantai: limit must be between 1 and 1000")
	}
	err = r.execute(ctx, func() error {
		var err error
		batch, err = r.upstream.Observations(ctx, cursor, limit, correlationID)
		if err != nil {
			return err
		}
		if batch.Invalid != nil {
			return batch.Invalid
		}
		if r.config.CheckPage != nil {
			if err := r.config.CheckPage(batch); err != nil {
				return fmt.Errorf("%w: %w", ErrDataHealth, err)
			}
		}
		return nil
	})
	return batch, err
}
