package satellite

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

// Repository is already implemented by store.Router. Raw state and pending
// effects belong to Aggregator's replicated canonical store, never local disk.
type Repository interface {
	LoadCursor(context.Context, string) (string, error)
	SaveCursorReplicated(context.Context, int64, string, string) error
	UpsertReplicated(context.Context, int64, []domain.HazardEvent, string) ([]domain.HazardEvent, error)
}

type recoveryCheckpoint struct {
	Version int
	State   *State
	Pending Projection
	// BaseID names this full checkpoint so a delta is only applied to its own base.
	BaseID string `json:",omitempty"`
}

// deltaSource holds the changes since the last full checkpoint. Rewriting the
// full state on every batch saturated the store links once it reached MBs.
const deltaSource = "SATELIT_DELTA"

// stateDelta is cumulative since BaseID: nil map values are deletions and
// State carries the scalar fields (its maps are ignored).
type stateDelta struct {
	BaseID     string
	State      State
	Pending    Projection
	Detections map[string]*Detection
	Owners     map[string]*string
	Seen       map[string]*bool
	Events     map[string]*domain.HazardEvent
}

func diffMap[V comparable](base, next map[string]V) map[string]*V {
	delta := map[string]*V{}
	for key, value := range next {
		if old, ok := base[key]; !ok || old != value {
			delta[key] = &value
		}
	}
	for key := range base {
		if _, ok := next[key]; !ok {
			delta[key] = nil
		}
	}
	return delta
}

func applyMap[V any](target map[string]V, delta map[string]*V) {
	for key, value := range delta {
		if value == nil {
			delete(target, key)
		} else {
			target[key] = *value
		}
	}
}

func diffState(base, next *State) stateDelta {
	scalars := *next
	scalars.Detections, scalars.Owners, scalars.Seen, scalars.Events = nil, nil, nil, nil
	delta := stateDelta{State: scalars, Detections: diffMap(base.Detections, next.Detections),
		Owners: diffMap(base.Owners, next.Owners), Seen: diffMap(base.Seen, next.Seen), Events: map[string]*domain.HazardEvent{}}
	for id, event := range next.Events {
		old, ok := base.Events[id]
		a, _ := json.Marshal(old)
		b, _ := json.Marshal(event)
		if !ok || !bytes.Equal(a, b) {
			delta.Events[id] = &event
		}
	}
	for id := range base.Events {
		if _, ok := next.Events[id]; !ok {
			delta.Events[id] = nil
		}
	}
	return delta
}

func (d stateDelta) applyTo(checkpoint *recoveryCheckpoint) {
	state := d.State
	state.Detections, state.Owners, state.Seen, state.Events = checkpoint.State.Detections, checkpoint.State.Owners, checkpoint.State.Seen, checkpoint.State.Events
	if state.Seen == nil {
		state.Seen = map[string]bool{}
	}
	applyMap(state.Detections, d.Detections)
	applyMap(state.Owners, d.Owners)
	applyMap(state.Seen, d.Seen)
	applyMap(state.Events, d.Events)
	checkpoint.State, checkpoint.Pending = &state, d.Pending
}

// Processor is single-writer and owned by one fenced leader term.
type Processor struct {
	repo       Repository
	epoch      int64
	verify     func(context.Context) error
	lateness   time.Duration
	checkpoint recoveryCheckpoint
	// base is the last durable full checkpoint state; deltas are diffed against it.
	base       *State
	baseID     string
	baseSize   int
	progress   atomic.Pointer[satelliteProgress]
	session    atomic.Pointer[Session]
}

// Option configures one processor instance without changing existing callers.
type Option func(*Processor)

// WithLateness sets the event-time window accepted from the feed. The
// deployment configuration is authoritative and is also applied on restore.
func WithLateness(lateness time.Duration) Option {
	return func(p *Processor) { p.lateness = lateness }
}

func NewProcessor(repo Repository, epoch int64, verify func(context.Context) error, options ...Option) (*Processor, error) {
	if repo == nil || verify == nil {
		return nil, errors.New("satellite: repository and term verifier required")
	}
	processor := &Processor{repo: repo, epoch: epoch, verify: verify, lateness: DefaultLateness}
	for _, option := range options {
		option(processor)
	}
	if processor.lateness <= 0 {
		return nil, errors.New("satellite: lateness must be positive")
	}
	processorReady.Set(0)
	offsetLag.Set(-1)
	eventLag.Set(-1)
	return processor, nil
}

func (p *Processor) save(ctx context.Context, checkpoint recoveryCheckpoint) error {
	if err := p.verify(ctx); err != nil {
		return err
	}
	if p.base != nil {
		delta := diffState(p.base, checkpoint.State)
		delta.BaseID, delta.Pending = p.baseID, checkpoint.Pending
		raw, err := json.Marshal(delta)
		if err != nil {
			return err
		}
		// Rewrite the base once the cumulative delta reaches 1/32 of it: this keeps
		// the average write near the minimum of base/T + delta*writes for a live feed.
		if len(raw) <= p.baseSize/32 {
			return p.repo.SaveCursorReplicated(ctx, p.epoch, deltaSource, string(raw))
		}
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	checkpoint.BaseID = hex.EncodeToString(id[:])
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	// Pruning bounds the state to roughly lateness + two cluster windows of detections.
	if len(raw) > 16<<20 {
		return errors.New("satellite: checkpoint exceeds 16 MiB; processing paused without advancing offset")
	}
	// The stale delta row keeps the old BaseID and is ignored on restore.
	if err := p.repo.SaveCursorReplicated(ctx, p.epoch, "SATELIT", string(raw)); err != nil {
		return err
	}
	p.base, p.baseID, p.baseSize = checkpoint.State, checkpoint.BaseID, len(raw)
	return nil
}

func (p *Processor) Restore(ctx context.Context) error {
	if err := p.verify(ctx); err != nil {
		return err
	}
	raw, err := p.repo.LoadCursor(ctx, "SATELIT")
	if err != nil {
		return err
	}
	checkpoint := recoveryCheckpoint{Version: 1, State: NewStateWithLateness(p.lateness)}
	if raw != "" {
		if len(raw) > 16<<20 {
			return errors.New("satellite: stored checkpoint exceeds 16 MiB")
		}
		if err := json.Unmarshal([]byte(raw), &checkpoint); err != nil {
			return fmt.Errorf("satellite checkpoint: %w", err)
		}
		if checkpoint.Version != 1 || checkpoint.State == nil || checkpoint.State.Offset < -1 || checkpoint.State.Lateness <= 0 || (checkpoint.State.Offset >= 0 && checkpoint.State.LastIngested.IsZero()) || checkpoint.State.Detections == nil || checkpoint.State.Owners == nil || checkpoint.State.Events == nil {
			return errors.New("satellite: invalid recovery checkpoint")
		}
		if err := applyDelta(ctx, p.repo, &checkpoint); err != nil {
			return err
		}
		// Configuration is authoritative; a restored checkpoint adopts the
		// deployment's lateness before any new effects are applied.
		checkpoint.State.Lateness = p.lateness
	}
	p.base = nil // the save below re-replicates a fresh full base
	// A previous failed RF acknowledgement may have left only one new copy.
	// Replicate that complete recovery record before applying any of its effects.
	if err := p.save(ctx, checkpoint); err != nil {
		return err
	}
	p.checkpoint = checkpoint
	if err := p.materialize(ctx); err != nil {
		return err
	}
	p.reportMetrics()
	return nil
}

type cursorLoader interface {
	LoadCursor(context.Context, string) (string, error)
}

// applyDelta rolls the base forward. A delta for another base (a newer base
// that this read did not see, or a stale row) is skipped: that is a safe replay.
func applyDelta(ctx context.Context, repo cursorLoader, checkpoint *recoveryCheckpoint) error {
	raw, err := repo.LoadCursor(ctx, deltaSource)
	if err != nil || raw == "" || checkpoint.BaseID == "" {
		return err
	}
	var delta stateDelta
	if err := json.Unmarshal([]byte(raw), &delta); err != nil {
		return fmt.Errorf("satellite checkpoint delta: %w", err)
	}
	if delta.BaseID == checkpoint.BaseID && delta.State.Offset >= checkpoint.State.Offset {
		delta.applyTo(checkpoint)
	}
	return nil
}

func (p *Processor) materialize(ctx context.Context) error {
	checkpoint := p.checkpoint
	if len(checkpoint.Pending.Upserts) == 0 && len(checkpoint.Pending.Retracted) == 0 {
		return nil
	}
	if err := p.verify(ctx); err != nil {
		return err
	}
	events := append([]domain.HazardEvent(nil), checkpoint.Pending.Upserts...)
	for _, previous := range checkpoint.Pending.Retracted {
		// Retain a replicated tombstone so anti-entropy cannot resurrect a withdrawn
		// fire from an older replica. Read/Audit paths exclude this internal marker.
		tombstone := previous
		tombstone.IngestedAt = checkpoint.State.LastIngested
		tombstone.Attributes = map[string]any{"retracted": true, "detection_count": 0}
		events = append(events, tombstone)
	}
	id := fmt.Sprintf("satellite-%d-%d", p.epoch, checkpoint.State.Offset)
	if _, err := p.repo.UpsertReplicated(ctx, p.epoch, events, id); err != nil {
		return err
	}
	checkpoint.Pending = Projection{}
	if err := p.save(ctx, checkpoint); err != nil {
		return err
	}
	p.checkpoint = checkpoint
	return nil
}

func (p *Processor) Offset() (int64, error) {
	if p.checkpoint.State == nil {
		return -1, errors.New("satellite: processor has not restored state")
	}
	if len(p.checkpoint.Pending.Upserts) > 0 || len(p.checkpoint.Pending.Retracted) > 0 {
		return -1, errors.New("satellite: pending effects must be recovered before streaming")
	}
	return p.checkpoint.State.Offset, nil
}

func (p *Processor) Process(ctx context.Context, batch []Mutation, ingested time.Time) error {
	return p.process(ctx, batch, ingested, time.Now())
}

func (p *Processor) process(ctx context.Context, batch []Mutation, ingested, received time.Time) (err error) {
	ctx, span := otel.Tracer("aggregator/satellite").Start(ctx, "satellite.process")
	effectful := 0
	defer func() {
		if err != nil {
			processorReady.Set(0)
			span.RecordError(err)
		} else {
			for range effectful {
				commitLatency.Observe(time.Since(received).Seconds())
			}
			p.reportMetrics()
		}
		span.End()
	}()
	if p.checkpoint.State == nil {
		return errors.New("satellite: restore processor before use")
	}
	if err := p.materialize(ctx); err != nil {
		return err
	}
	next, projection, err := p.checkpoint.State.Next(batch, ingested)
	if err != nil {
		return err
	}
	if next.Offset == p.checkpoint.State.Offset {
		return nil
	}
	effectful = len(projection.Upserts) + len(projection.Retracted)
	checkpoint := recoveryCheckpoint{Version: 1, State: next, Pending: projection}
	// Write-ahead checkpoint: the offset, raw detections, identity ledger and
	// pending effects survive a crash before any HazardEvent is materialized.
	if err := p.save(ctx, checkpoint); err != nil {
		return err
	}
	p.checkpoint = checkpoint
	return p.materialize(ctx)
}
