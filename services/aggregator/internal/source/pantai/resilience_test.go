package pantai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type fakeUpstream struct {
	calls atomic.Int32
	call  func(context.Context) error
	batch Batch
}

func (f *fakeUpstream) Stations(ctx context.Context, _ string) ([]Station, error) {
	f.calls.Add(1)
	return nil, f.call(ctx)
}

func (f *fakeUpstream) Observations(ctx context.Context, _ string, _ int, _ string) (Batch, error) {
	f.calls.Add(1)
	return f.batch, f.call(ctx)
}

type resilienceRig struct {
	r     *Resilient
	nanos atomic.Int64
	waits []time.Duration
}

func newRig(t *testing.T, upstream Upstream, configure func(*ResilienceConfig)) *resilienceRig {
	t.Helper()
	config := DefaultResilienceConfig()
	if configure != nil {
		configure(&config)
	}
	r, err := NewResilient(upstream, config)
	if err != nil {
		t.Fatal(err)
	}
	rig := &resilienceRig{r: r}
	rig.nanos.Store(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC).UnixNano())
	r.now = func() time.Time { return time.Unix(0, rig.nanos.Load()).UTC() }
	r.jitter = func(delay time.Duration) time.Duration { return delay }
	r.wait = func(ctx context.Context, delay time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		rig.waits = append(rig.waits, delay)
		rig.advance(delay)
		return nil
	}
	return rig
}

func (r *resilienceRig) advance(delay time.Duration) { r.nanos.Add(int64(delay)) }

func TestResilienceOpensRejectsAndRecoversWithSingleProbe(t *testing.T) {
	fail := true
	fake := &fakeUpstream{call: func(context.Context) error {
		if fail {
			return &HTTPError{StatusCode: 503}
		}
		return nil
	}}
	rig := newRig(t, fake, nil)
	for i := 0; i < 3; i++ {
		if _, err := rig.r.Observations(context.Background(), "", 10, "corr"); err == nil {
			t.Fatal("upstream failure accepted")
		}
		rig.advance(2 * time.Second)
	}
	if status := rig.r.Status(); status.State != "open" || status.ConsecutiveFailures != 3 {
		t.Fatalf("breaker not open: %+v", status)
	}
	before := fake.calls.Load()
	_, err := rig.r.Stations(context.Background(), "corr")
	var gate *GateError
	if !errors.As(err, &gate) || gate.Reason != "circuit_open" || fake.calls.Load() != before {
		t.Fatalf("open circuit called upstream: %v", err)
	}
	rig.advance(10 * time.Second)
	_, _ = rig.r.Observations(context.Background(), "", 10, "corr")
	if fake.calls.Load() != before+1 || rig.r.Status().State != "open" {
		t.Fatal("failed half-open probe retried or did not reopen")
	}
	fail = false
	rig.advance(10 * time.Second)
	if _, err := rig.r.Observations(context.Background(), "", 10, "corr"); err != nil {
		t.Fatal(err)
	}
	status := rig.r.Status()
	if status.State != "closed" || status.ConsecutiveFailures != 0 || !status.RetryAt.IsZero() || status.LastSuccess.IsZero() {
		t.Fatalf("recovery did not close/reset the breaker: %+v", status)
	}
}

func TestResilienceBulkheadAndCanceledHalfOpen(t *testing.T) {
	entered := make(chan struct{})
	fake := &fakeUpstream{call: func(context.Context) error { return &HTTPError{StatusCode: 503} }}
	rig := newRig(t, fake, func(c *ResilienceConfig) { c.MaxAttempts = 1; c.FailureThreshold = 1 })
	_, _ = rig.r.Observations(context.Background(), "", 10, "corr")
	rig.advance(10 * time.Second)
	fake.call = func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := rig.r.Observations(ctx, "", 10, "corr"); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	status := rig.r.Status()
	if status.State != "half_open" || !status.InFlight {
		t.Fatalf("probe not visible in status: %+v", status)
	}
	_, err := rig.r.Stations(context.Background(), "other")
	var gate *GateError
	if !errors.As(err, &gate) || gate.Reason != "bulkhead_full" || fake.calls.Load() != 2 {
		t.Fatal("concurrent request escaped the bulkhead")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	if status := rig.r.Status(); status.State != "open" || status.InFlight || status.ConsecutiveFailures != 1 {
		t.Fatalf("canceled probe proved false recovery or leaked permit: %+v", status)
	}
}

func TestResilienceRetryBudgetSharedAcrossEndpointsAndRefills(t *testing.T) {
	fake := &fakeUpstream{call: func(context.Context) error { return &HTTPError{StatusCode: 503} }}
	rig := newRig(t, fake, func(c *ResilienceConfig) { c.MaxAttempts = 3; c.FailureThreshold = 10 })
	_, _ = rig.r.Stations(context.Background(), "first")
	if fake.calls.Load() != 3 || len(rig.waits) != 2 || rig.waits[0] != 250*time.Millisecond || rig.waits[1] != 500*time.Millisecond {
		t.Fatalf("attempt/backoff budget: calls=%d waits=%v", fake.calls.Load(), rig.waits)
	}
	rig.advance(2 * time.Second)
	_, _ = rig.r.Observations(context.Background(), "", 10, "second")
	if fake.calls.Load() != 4 || rig.r.Status().Retries != 2 {
		t.Fatal("observation endpoint bypassed exhausted station retry budget")
	}
	rig.advance(10 * time.Second)
	_, _ = rig.r.Observations(context.Background(), "", 10, "third")
	if fake.calls.Load() != 7 || rig.r.Status().Retries != 4 {
		t.Fatal("retry budget did not replenish after its window")
	}
}

func TestResilienceHonorsLongRetryAfterWithoutWaiting(t *testing.T) {
	fail := true
	fake := &fakeUpstream{call: func(context.Context) error {
		if fail {
			return &HTTPError{StatusCode: 429, RetryAfter: "25"}
		}
		return nil
	}}
	rig := newRig(t, fake, nil)
	now := rig.r.now()
	_, _ = rig.r.Observations(context.Background(), "", 10, "corr")
	if fake.calls.Load() != 1 || len(rig.waits) != 0 || !rig.r.Status().RetryAt.Equal(now.Add(25*time.Second)) {
		t.Fatal("long Retry-After was ignored or held the caller")
	}
	rig.advance(12 * time.Second)
	_, err := rig.r.Stations(context.Background(), "corr")
	var gate *GateError
	if !errors.As(err, &gate) || fake.calls.Load() != 1 {
		t.Fatal("request sent before Retry-After elapsed")
	}
	fail = false
	rig.advance(13 * time.Second)
	if _, err := rig.r.Stations(context.Background(), "corr"); err != nil {
		t.Fatal(err)
	}
}

func TestResilienceRetryAfterDateAndMalformedHints(t *testing.T) {
	for _, hint := range []string{"date", "garbage", "-1"} {
		t.Run(hint, func(t *testing.T) {
			fake := &fakeUpstream{}
			rig := newRig(t, fake, nil)
			fake.call = func(context.Context) error {
				if fake.calls.Load() > 1 {
					return nil
				}
				value := hint
				if hint == "date" {
					value = rig.r.now().Add(time.Second).Format(http.TimeFormat)
				}
				return &HTTPError{StatusCode: 503, RetryAfter: value}
			}
			if _, err := rig.r.Stations(context.Background(), "corr"); err != nil {
				t.Fatal(err)
			}
			want := 250 * time.Millisecond
			if hint == "date" {
				want = time.Second
			}
			if len(rig.waits) != 1 || rig.waits[0] != want {
				t.Fatalf("unexpected retry delay: %v", rig.waits)
			}
		})
	}
}

func TestResilienceDoesNotRetryPermanentOrDataFailures(t *testing.T) {
	for _, cause := range []error{&HTTPError{StatusCode: 401}, violation("malformed_json"), ErrLeaseVerification} {
		fake := &fakeUpstream{call: func(context.Context) error { return cause }}
		rig := newRig(t, fake, nil)
		_, err := rig.r.Observations(context.Background(), "", 10, "corr")
		if !errors.Is(err, cause) || fake.calls.Load() != 1 || len(rig.waits) != 0 {
			t.Fatalf("non-retryable failure retried: %v", err)
		}
		if cause == ErrLeaseVerification && rig.r.Status().ConsecutiveFailures != 0 {
			t.Fatal("lost lease counted as a sensor failure")
		}
	}
	raw := []byte("broken original body")
	fake := &fakeUpstream{call: func(context.Context) error { return nil }, batch: Batch{Raw: raw, Invalid: violation("malformed_envelope")}}
	rig := newRig(t, fake, nil)
	batch, err := rig.r.Observations(context.Background(), "", 10, "corr")
	if err == nil || string(batch.Raw) != string(raw) || fake.calls.Load() != 1 || rig.r.Status().LastFailure.IsZero() {
		t.Fatal("invalid envelope lost quarantine bytes or retried")
	}
	fake.batch = Batch{}
	rig = newRig(t, fake, func(c *ResilienceConfig) {
		c.FailureThreshold = 1
		c.CheckPage = func(Batch) error { return context.DeadlineExceeded }
	})
	before := fake.calls.Load()
	if _, err := rig.r.Observations(context.Background(), "", 10, "corr"); !errors.Is(err, ErrDataHealth) || fake.calls.Load() != before+1 {
		t.Fatal("gray failure was accepted or caused immediate retry")
	}
	if rig.r.Status().State != "open" {
		t.Fatal("HTTP 200 with failed data health falsely kept the circuit healthy")
	}
}

func TestResilienceValidSiblingsProceedDespitePoisonRecord(t *testing.T) {
	batch := DecodeBatch(envelope(`null,` + validObservation))
	fake := &fakeUpstream{batch: batch, call: func(context.Context) error { return nil }}
	rig := newRig(t, fake, nil)
	got, err := rig.r.Observations(context.Background(), "", 10, "corr")
	if err != nil || len(got.Records) != 2 || got.Records[0].Invalid == nil || got.Records[1].Invalid != nil || string(got.Records[0].Raw) != "null" {
		t.Fatal("poison record blocked its valid sibling or lost its DLQ payload")
	}
	if fake.calls.Load() != 1 || rig.r.Status().State != "closed" {
		t.Fatal("partial contract failure retried the whole page")
	}
}

func TestResilienceRealHTTPTimeoutHasBoundedRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server, func(c *Config) { c.Timeout = 25 * time.Millisecond })
	rig := newRig(t, client, func(c *ResilienceConfig) { c.FailureThreshold = 1 })
	_, err := rig.r.Observations(context.Background(), "", 10, "corr")
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 2 || rig.r.Status().Retries != 1 || rig.r.Status().State != "open" {
		t.Fatalf("timeout retry limit violated: calls=%d status=%+v err=%v", calls.Load(), rig.r.Status(), err)
	}
}

func TestResilienceCancellationDuringBackoffReleasesPermit(t *testing.T) {
	fake := &fakeUpstream{call: func(context.Context) error { return &HTTPError{StatusCode: 503} }}
	rig := newRig(t, fake, nil)
	ctx, cancel := context.WithCancel(context.Background())
	rig.r.wait = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	_, err := rig.r.Stations(ctx, "corr")
	if !errors.Is(err, context.Canceled) || fake.calls.Load() != 1 || rig.r.Status().InFlight || rig.r.Status().ConsecutiveFailures != 0 {
		t.Fatalf("backoff cancellation leaked or retried: %v %+v", err, rig.r.Status())
	}
}

func TestResilienceRealHTTPOutageDoesNotBlockOtherSource(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)
	rig := newRig(t, client, func(c *ResilienceConfig) { c.FailureThreshold = 1; c.MaxAttempts = 1 })
	_, _ = rig.r.Observations(context.Background(), "", 10, "corr")
	for i := 0; i < 100; i++ {
		_, _ = rig.r.Observations(context.Background(), "", 10, "corr")
	}
	if calls.Load() != 1 || rig.r.Status().Rejected != 100 {
		t.Fatal("open breaker produced an HTTP request storm")
	}
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(envelope(validObservation)) }))
	defer healthy.Close()
	other := newTestClient(t, healthy, nil)
	if _, err := other.Observations(context.Background(), "", 10, "other"); err != nil {
		t.Fatalf("independent source was affected: %v", err)
	}
}

func TestResilienceJitterBoundsAndInvalidConfiguration(t *testing.T) {
	fake := &fakeUpstream{call: func(context.Context) error { return nil }}
	r, err := NewResilient(fake, DefaultResilienceConfig())
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 100; attempt++ {
		for i := 0; i < 100; i++ {
			delay := r.delay(attempt)
			if delay < r.config.RetryBase/2 || delay > r.config.RetryMax {
				t.Fatalf("jitter out of bounds: %v", delay)
			}
		}
	}
	config := DefaultResilienceConfig()
	config.RetryBase, config.RetryMax = time.Nanosecond, time.Nanosecond
	tiny, err := NewResilient(fake, config)
	if err != nil || tiny.delay(0) != time.Nanosecond {
		t.Fatal("positive sub-millisecond backoff became a zero-delay retry")
	}
	config = DefaultResilienceConfig()
	config.MaxAttempts = 0
	if _, err := NewResilient(fake, config); err == nil {
		t.Fatal("zero attempts accepted")
	}
}
