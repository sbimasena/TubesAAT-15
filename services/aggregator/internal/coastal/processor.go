// Package coastal owns leader-only Sensor Pantai ingestion. Source I/O remains
// in source/pantai; durable writes remain inside the Aggregator store router.
package coastal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/ingest"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source/pantai"
)

const Source = "SENSOR_PANTAI"

var (
	errVirtualClockStalled = errors.New("sensor virtual clock stopped progressing")
	errSessionUnconfirmed  = errors.New("sensor clock regression not confirmed from the first page")
	errInvalidCheckpoint   = errors.New("invalid coastal checkpoint")
)

// storageError identifies the failed operation without logging raw driver errors.
type storageError struct {
	operation string
	cause     error
}

func (e *storageError) Error() string { return e.operation + ": " + e.cause.Error() }
func (e *storageError) Unwrap() error { return e.cause }

type Repository interface {
	UpsertReplicated(context.Context, int64, []domain.HazardEvent, string) ([]domain.HazardEvent, error)
	RecordUnprocessed(context.Context, int64, deadletter.Message) error
	SaveCursorReplicated(context.Context, int64, string, string) error
	LoadCursor(context.Context, string) (string, error)
}

type checkpoint struct {
	Cursor         string              `json:"cursor"`
	CursorHistory  []string            `json:"cursor_history"`
	GeneratedAt    time.Time           `json:"generated_at"`
	LastProgressAt time.Time           `json:"last_progress_at"`
	Status         ingest.SourceStatus `json:"status"`
}

type Processor struct {
	repo       Repository
	client     *pantai.Resilient
	epoch      int64
	verify     func(context.Context) error
	state      checkpoint
	stations   map[string]pantai.Station
	refreshAt  time.Time
	staleAfter time.Duration
	limit      int
	now        func() time.Time
}

func NewProcessor(repo Repository, upstream pantai.Upstream, epoch int64, verify func(context.Context) error, staleAfter time.Duration, limit int, config pantai.ResilienceConfig) (*Processor, error) {
	if repo == nil || verify == nil || staleAfter <= 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid coastal processor configuration")
	}
	p := &Processor{repo: repo, epoch: epoch, verify: verify, staleAfter: staleAfter, limit: limit, now: time.Now}
	config.CheckPage = p.checkPage
	var err error
	p.client, err = pantai.NewResilient(upstream, config)
	return p, err
}

func (p *Processor) Restore(ctx context.Context) error {
	raw, err := p.repo.LoadCursor(ctx, Source)
	if err != nil {
		return &storageError{"load checkpoint", err}
	}
	var state checkpoint
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return errInvalidCheckpoint
		}
		if len(state.CursorHistory) > 64 {
			return fmt.Errorf("%w: cursor history exceeds limit", errInvalidCheckpoint)
		}
	}
	p.state = state
	return nil
}

func cursorHash(cursor string) string {
	hash := sha256.Sum256([]byte(cursor))
	return hex.EncodeToString(hash[:])
}

func cursorLoops(state checkpoint, batch pantai.Batch) bool {
	if batch.NextCursor == nil {
		return false
	}
	next := *batch.NextCursor
	if next == state.Cursor {
		return len(batch.Records) != 0
	}
	hash := cursorHash(next)
	for _, previous := range state.CursorHistory {
		if hash == previous {
			return true
		}
	}
	return false
}

func (p *Processor) checkPage(cursor string, batch pantai.Batch) error {
	if batch.NextCursor != nil && len(*batch.NextCursor) > 4096 {
		return &pantai.Violation{Detail: "cursor_too_large"}
	}
	state := p.state
	if cursor == "" && (p.state.Cursor != "" || batch.GeneratedAt.Before(p.state.GeneratedAt)) {
		state = checkpoint{}
	} else if batch.GeneratedAt.Before(p.state.GeneratedAt) {
		// A structurally valid page at the old cursor is only a restart signal.
		// Its records and cursor are never used for replay.
		return nil
	}
	if cursorLoops(state, batch) {
		return &pantai.Violation{Detail: "cursor_not_advancing"}
	}
	if batch.GeneratedAt.Equal(state.GeneratedAt) && !state.LastProgressAt.IsZero() &&
		p.now().Sub(state.LastProgressAt) >= p.staleAfter {
		return errVirtualClockStalled
	}
	return nil
}

// MapObservation applies only the provisional policy approved for this project:
// quality=ok, nonnegative waves, known station, no time after generated_at,
// critical->AWAS, otherwise NORMAL; province supplies the public area name.
// Numeric upper bounds and an absolute virtual-time clock remain unspecified.
func MapObservation(record pantai.Record, stations map[string]pantai.Station, generatedAt, now time.Time) (domain.HazardEvent, error) {
	if record.Invalid != nil {
		return domain.HazardEvent{}, record.Invalid
	}
	o := record.Observation
	station, ok := stations[o.StationID]
	var detail string
	switch {
	case strings.TrimSpace(o.ObservationID) == "" || o.ObservedAt.IsZero():
		detail = "observation_identity"
	case !ok:
		detail = "unknown_station"
	case o.Quality != "ok":
		detail = "quality"
	case math.IsNaN(o.SeaLevelAnomalyM) || math.IsInf(o.SeaLevelAnomalyM, 0) || math.IsNaN(o.WaveHeightM) || math.IsInf(o.WaveHeightM, 0):
		detail = "nonfinite_measurement"
	case o.WaveHeightM < 0:
		detail = "negative_wave_height"
	case o.ObservedAt.After(generatedAt):
		detail = "future_observation"
	}
	if detail != "" {
		return domain.HazardEvent{}, &pantai.Violation{Detail: detail}
	}
	severity := "NORMAL"
	if o.Critical {
		severity = "AWAS"
	}
	return domain.HazardEvent{HazardID: domain.HazardID(Source, o.ObservationID), Source: Source,
		SourceRefID: o.ObservationID, HazardType: "COASTAL", Severity: severity, AreaName: station.Province,
		Latitude: station.Latitude, Longitude: station.Longitude, OccurredAt: o.ObservedAt.UTC(), IngestedAt: now.UTC(),
		Attributes: map[string]any{"station_id": o.StationID, "station_name": station.Name,
			"sea_level_anomaly_m": o.SeaLevelAnomalyM, "wave_height_m": o.WaveHeightM, "quality": o.Quality, "critical": o.Critical}}, nil
}

func (p *Processor) park(ctx context.Context, cursor string, raw []byte, ref string, cause error) error {
	if err := p.verify(ctx); err != nil {
		return err
	}
	reason := "contract_violation:invalid_observation"
	var violation *pantai.Violation
	if errors.As(cause, &violation) {
		reason = violation.Error()
	}
	hash := sha256.Sum256(raw)
	if err := p.repo.RecordUnprocessed(ctx, p.epoch, deadletter.Message{MessageID: fmt.Sprintf("pantai:%x", hash),
		Source: Source, SourceRefID: ref, Position: cursor, Raw: raw, Reason: reason,
		ReceivedAt: p.now().UTC(), Attempts: 1, Status: deadletter.StatusParked}); err != nil {
		return &storageError{"write DLQ", err}
	}
	return nil
}

func (p *Processor) refreshStations(ctx context.Context, correlation string, now time.Time) error {
	stations, err := p.client.Stations(ctx, correlation)
	if err != nil {
		return err
	}
	p.stations = make(map[string]pantai.Station, len(stations))
	for _, station := range stations {
		p.stations[station.StationID] = station
	}
	p.refreshAt = now.Add(time.Minute)
	return nil
}

// readPage returns a usable page and its optional freshness error, or a fatal
// error for this poll. Untrustworthy envelopes are never followed.
func (p *Processor) readPage(ctx context.Context, cursor, correlation string) (pantai.Batch, error, error) {
	batch, healthErr := p.client.Observations(ctx, cursor, p.limit, correlation)
	if batch.Invalid != nil {
		if !batch.Truncated {
			if err := p.park(ctx, cursor, batch.Raw, "", batch.Invalid); err != nil {
				return batch, nil, err
			}
		}
		return batch, nil, batch.Invalid
	}
	if healthErr != nil && !errors.Is(healthErr, pantai.ErrDataHealth) {
		return batch, nil, healthErr
	}
	var pageViolation *pantai.Violation
	if errors.As(healthErr, &pageViolation) {
		if err := p.park(ctx, cursor, batch.Raw, "", pageViolation); err != nil {
			return batch, nil, err
		}
		return batch, nil, healthErr
	}
	return batch, healthErr, nil
}

// Poll never changes the in-memory cursor until every effect and the replicated
// checkpoint commit. Retrying after partial writes is safe through stable IDs.
func (p *Processor) Poll(ctx context.Context, correlation string) error {
	if err := p.verify(ctx); err != nil {
		return err
	}
	now := p.now().UTC()
	if p.stations == nil || !now.Before(p.refreshAt) {
		if err := p.refreshStations(ctx, correlation, now); err != nil {
			return err
		}
	}
	base := p.state
	batch, healthErr, err := p.readPage(ctx, base.Cursor, correlation)
	if err != nil {
		return err
	}
	if batch.GeneratedAt.Before(base.GeneratedAt) {
		// Cursors and loop history belong to the old dataset. Confirm the
		// regression without a cursor before accepting any replacement data.
		if base.Cursor != "" {
			batch, healthErr, err = p.readPage(ctx, "", correlation)
			if err != nil {
				return err
			}
			if !batch.GeneratedAt.Before(base.GeneratedAt) {
				return errSessionUnconfirmed
			}
		}
		if err := p.refreshStations(ctx, correlation, now); err != nil {
			return err
		}
		base = checkpoint{}
	}
	events := make([]domain.HazardEvent, 0, len(batch.Records))
	var invalid bool
	first := make(map[string]pantai.Record)
	conflicting := make(map[string]bool)
	for _, record := range batch.Records {
		if _, err := MapObservation(record, p.stations, batch.GeneratedAt, now); err != nil {
			continue
		}
		id := record.Observation.ObservationID
		if previous, ok := first[id]; ok {
			a, b := previous.Observation, record.Observation
			a.ObservedAt, b.ObservedAt = a.ObservedAt.UTC(), b.ObservedAt.UTC()
			if a != b {
				conflicting[id] = true
			}
		} else {
			first[id] = record
		}
	}
	seen := make(map[string]bool)
	var previous pantai.Observation
	for _, record := range batch.Records {
		o := record.Observation
		if record.Invalid == nil && conflicting[o.ObservationID] {
			record.Invalid = &pantai.Violation{Detail: "conflicting_observation_id"}
		}
		if record.Invalid == nil && !previous.ObservedAt.IsZero() &&
			(o.ObservedAt.Before(previous.ObservedAt) || o.ObservedAt.Equal(previous.ObservedAt) && o.ObservationID < previous.ObservationID) {
			record.Invalid = &pantai.Violation{Detail: "observation_order"}
		}
		event, err := MapObservation(record, p.stations, batch.GeneratedAt, now)
		if err != nil {
			invalid = true
			if err := p.park(ctx, base.Cursor, record.Raw, record.Observation.ObservationID, err); err != nil {
				return err
			}
			continue
		}
		previous = o
		if seen[o.ObservationID] {
			continue // identical replay inside this page is not a new effect
		}
		seen[o.ObservationID] = true
		events = append(events, event)
	}
	if err := p.verify(ctx); err != nil {
		return err
	}
	if _, err := p.repo.UpsertReplicated(ctx, p.epoch, events, correlation); err != nil {
		return &storageError{"write hazards", err}
	}
	next := base
	next.CursorHistory = append([]string(nil), base.CursorHistory...)
	if batch.NextCursor != nil && *batch.NextCursor != base.Cursor {
		next.Cursor = *batch.NextCursor
		next.CursorHistory = append(next.CursorHistory, cursorHash(next.Cursor))
		if len(next.CursorHistory) > 64 {
			next.CursorHistory = next.CursorHistory[len(next.CursorHistory)-64:]
		}
	}
	if batch.GeneratedAt.After(next.GeneratedAt) {
		next.GeneratedAt, next.LastProgressAt = batch.GeneratedAt, now
	}
	next.Status = ingest.SourceStatus{Available: healthErr == nil, LastSuccess: &now, LastIngested: &now,
		Stale: invalid || healthErr != nil, StaleAfterSeconds: p.staleAfter.Seconds()}
	if next.Status.Stale {
		next.Status.StaleSince = &now
		if p.state.Status.StaleSince != nil {
			next.Status.StaleSince = p.state.Status.StaleSince
		}
		next.Status.IngestionError = "coastal data health or validation failed"
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return &storageError{"save checkpoint", err}
	}
	if err := p.verify(ctx); err != nil {
		return err
	}
	if err := p.repo.SaveCursorReplicated(ctx, p.epoch, Source, string(raw)); err != nil {
		return &storageError{"save checkpoint", err}
	}
	p.state = next
	return healthErr
}
