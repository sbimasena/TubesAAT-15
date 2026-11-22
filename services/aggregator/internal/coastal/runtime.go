package coastal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/ingest"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source/pantai"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type Config struct {
	BaseURL      string
	Token        string
	InstanceID   string
	Timeout      time.Duration
	PollInterval time.Duration
	StaleAfter   time.Duration
	PageLimit    int
	Resilience   pantai.ResilienceConfig
}

func DefaultConfig() Config {
	return Config{Timeout: 2 * time.Second, PollInterval: 500 * time.Millisecond, StaleAfter: 10 * time.Second,
		PageLimit: 100, Resilience: pantai.DefaultResilienceConfig()}
}

type Runtime struct {
	config Config
	repo   Repository
	client *pantai.Client
	verify func(context.Context) error
	logger *slog.Logger
	mu     sync.RWMutex
	status ingest.SourceStatus
	active bool
	guard  *pantai.Resilient
}

func NewRuntime(config Config, repo Repository, verify func(context.Context) error, logger *slog.Logger) (*Runtime, error) {
	if repo == nil || verify == nil || config.Timeout <= 0 || config.PollInterval <= 0 || config.StaleAfter <= 0 || config.PageLimit < 1 || config.PageLimit > 1000 {
		return nil, fmt.Errorf("invalid coastal runtime configuration")
	}
	if err := config.Resilience.Validate(); err != nil {
		return nil, err
	}
	client, err := pantai.New(pantai.Config{BaseURL: config.BaseURL, Token: config.Token, Timeout: config.Timeout,
		InstanceID: config.InstanceID, Verify: verify, Logger: logger,
		WrapTransport: func(pool http.RoundTripper) http.RoundTripper { return otelhttp.NewTransport(pool) }})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &Runtime{config: config, repo: repo, client: client, verify: verify, logger: logger,
		status: ingest.SourceStatus{Stale: true, StaleSince: &now, StaleAfterSeconds: config.StaleAfter.Seconds()}}, nil
}

func (r *Runtime) Close() { r.client.Close() }

func (r *Runtime) setStatus(status ingest.SourceStatus) {
	r.mu.Lock()
	r.status = status
	r.mu.Unlock()
}

func (r *Runtime) Status() ingest.SourceStatus {
	r.mu.RLock()
	status := r.status
	r.mu.RUnlock()
	status.StaleAfterSeconds = r.config.StaleAfter.Seconds()
	now := time.Now().UTC()
	if status.LastIngested == nil || now.Sub(*status.LastIngested) >= r.config.StaleAfter {
		status.Stale = true
		if status.StaleSince == nil {
			deadline := now
			if status.LastIngested != nil {
				deadline = status.LastIngested.Add(r.config.StaleAfter)
			}
			status.StaleSince = &deadline
		}
	}
	return status
}

func (r *Runtime) Detail() any {
	r.mu.RLock()
	guard, active := r.guard, r.active
	r.mu.RUnlock()
	var breaker *pantai.ResilienceStatus
	if active && guard != nil {
		status := guard.Status()
		breaker = &status
	}
	return struct {
		Active  bool                     `json:"leader_worker_active"`
		Source  ingest.SourceStatus      `json:"source"`
		Breaker *pantai.ResilienceStatus `json:"breaker"`
	}{active, r.Status(), breaker}
}

func (r *Runtime) Run(ctx context.Context, epoch int64) error {
	p, err := NewProcessor(r.repo, r.client, epoch, r.verify, r.config.StaleAfter, r.config.PageLimit, r.config.Resilience)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.active, r.guard = true, p.client
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.active = false; r.mu.Unlock() }()
	restored := false
	for ctx.Err() == nil {
		if err := r.verify(ctx); err != nil {
			return err
		}
		if !restored {
			err = p.Restore(ctx)
			restored = err == nil
		}
		if restored {
			var id [12]byte
			if _, err = rand.Read(id[:]); err != nil {
				return err
			}
			previous := p.state.GeneratedAt
			err = p.Poll(ctx, "pantai-"+hex.EncodeToString(id[:]))
			if err == nil && p.state.GeneratedAt.Before(previous) && r.logger != nil {
				r.logger.Info("coastal session restarted", "source", Source, "epoch", epoch,
					"previous_generated_at", previous, "generated_at", p.state.GeneratedAt)
			}
		}
		if errors.Is(err, store.ErrFenced) || errors.Is(err, pantai.ErrLeaseVerification) || ctx.Err() != nil {
			return err
		}
		if err != nil {
			now := time.Now().UTC()
			status := p.state.Status
			status.Available, status.Stale, status.LastError = false, true, "coastal ingestion unavailable"
			if status.StaleSince == nil {
				status.StaleSince = &now
			}
			r.setStatus(status)
			// Persist failure metadata using the OLD checkpoint position. A
			// failed data/DLQ write cannot accidentally release an observation.
			if restored && r.verify(ctx) == nil {
				state := p.state
				state.Status = status
				if raw, encodeErr := json.Marshal(state); encodeErr == nil {
					if saveErr := r.repo.SaveCursorReplicated(ctx, epoch, Source, string(raw)); saveErr == nil {
						p.state = state
					}
				}
			}
			if r.logger != nil {
				r.logger.Warn("coastal cycle deferred", "source", Source, "epoch", epoch,
					"failure_type", fmt.Sprintf("%T", err), "reason", summarizeFailure(err))
			}
		} else {
			r.setStatus(p.state.Status)
		}
		delay := r.config.PollInterval
		var gate *pantai.GateError
		if errors.As(err, &gate) && gate.RetryAt.After(time.Now()) {
			delay = max(delay, time.Until(gate.RetryAt))
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Never log arbitrary HTTP or database error strings: they may include URLs,
// cursor parameters, credentials or raw payloads.
func summarizeFailure(err error) string {
	var storage *storageError
	var violation *pantai.Violation
	var upstream *pantai.HTTPError
	var gate *pantai.GateError
	var dns *net.DNSError
	var network net.Error
	switch {
	case errors.As(err, &storage):
		if errors.Is(err, context.DeadlineExceeded) {
			return "storage timeout: " + storage.operation
		}
		return "storage unavailable: " + storage.operation
	case errors.As(err, &violation):
		return violation.Error()
	case errors.As(err, &upstream):
		return upstream.Error()
	case errors.As(err, &gate):
		return gate.Error()
	case errors.Is(err, errVirtualClockStalled):
		return errVirtualClockStalled.Error()
	case errors.Is(err, errSessionUnconfirmed):
		return errSessionUnconfirmed.Error()
	case errors.Is(err, errInvalidCheckpoint):
		return errInvalidCheckpoint.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		return "sensor request timed out"
	case errors.As(err, &dns):
		return "sensor DNS lookup failed"
	case errors.As(err, &network):
		return "sensor transport failed"
	default:
		return "coastal operation failed"
	}
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RefreshStatus only reads committed metadata on followers. It never polls the
// sensor and never makes a follower look fresh merely because storage answered.
func (r *Runtime) RefreshStatus(ctx context.Context, isLeader func() bool) {
	for ctx.Err() == nil {
		if !isLeader() {
			if raw, err := r.repo.LoadCursor(ctx, Source); err == nil && raw != "" {
				var state checkpoint
				if json.Unmarshal([]byte(raw), &state) == nil && !isLeader() {
					r.mu.Lock()
					if !r.active {
						r.status = state.Status
					}
					r.mu.Unlock()
				}
			}
		}
		if wait(ctx, r.config.PollInterval) != nil {
			return
		}
	}
}
