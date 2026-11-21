package coastal

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source/pantai"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

const normalRecord = `{"observation_id":"independent-ref","station_id":"station-test","observed_at":"2026-11-12T00:00:00Z","sea_level_anomaly_m":0.2,"wave_height_m":1,"quality":"ok","critical":false}`

func page(items, next string) pantai.Batch {
	return pantai.DecodeBatch([]byte(`{"generated_at":"2026-11-12T00:01:00Z","items":[` + items + `],"next_cursor":` + next + `}`))
}

type fakeSource struct {
	batch  pantai.Batch
	fetch  func(string) (pantai.Batch, error)
	cursor string
	calls  int
}

func (f *fakeSource) Stations(context.Context, string) ([]pantai.Station, error) {
	return []pantai.Station{{StationID: "station-test", Name: "Test Coast", Province: "Test Province", Latitude: -1, Longitude: 100}}, nil
}
func (f *fakeSource) Observations(_ context.Context, cursor string, _ int, _ string) (pantai.Batch, error) {
	f.calls++
	f.cursor = cursor
	if f.fetch != nil {
		return f.fetch(cursor)
	}
	return f.batch, nil
}

type memoryRepo struct {
	cursor  string
	events  map[string]domain.HazardEvent
	dlq     map[string]deadletter.Message
	fail    string
	effects []string
}

func (m *memoryRepo) LoadCursor(context.Context, string) (string, error) { return m.cursor, nil }
func (m *memoryRepo) UpsertReplicated(_ context.Context, _ int64, events []domain.HazardEvent, _ string) ([]domain.HazardEvent, error) {
	m.effects = append(m.effects, "hazards")
	if m.fail == "hazards" {
		return nil, errors.New("store unavailable")
	}
	if m.fail == "fence" {
		return nil, store.ErrFenced
	}
	for _, event := range events {
		m.events[event.HazardID] = event
	}
	return events, nil
}
func (m *memoryRepo) RecordUnprocessed(_ context.Context, _ int64, msg deadletter.Message) error {
	m.effects = append(m.effects, "dlq")
	if m.fail == "dlq" {
		return errors.New("DLQ unavailable")
	}
	m.dlq[msg.MessageID] = msg
	return nil
}
func (m *memoryRepo) SaveCursorReplicated(_ context.Context, _ int64, _, raw string) error {
	m.effects = append(m.effects, "checkpoint")
	if m.fail == "checkpoint" {
		return errors.New("checkpoint unavailable")
	}
	m.cursor = raw
	return nil
}

func processor(t *testing.T, source *fakeSource, repo Repository) *Processor {
	t.Helper()
	p, err := NewProcessor(repo, source, 3, func(context.Context) error { return nil }, 10*time.Second, 100, pantai.DefaultResilienceConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p
}

func newMemory() *memoryRepo {
	return &memoryRepo{events: map[string]domain.HazardEvent{}, dlq: map[string]deadletter.Message{}}
}

func TestCoastalMappingAndSemanticQuarantine(t *testing.T) {
	critical := strings.Replace(strings.Replace(normalRecord, "independent-ref", "critical-ref", 1), `"critical":false`, `"critical":true`, 1)
	negative := strings.Replace(normalRecord, `"wave_height_m":1`, `"wave_height_m":-1`, 1)
	quality := strings.Replace(normalRecord, `"quality":"ok"`, `"quality":"bad"`, 1)
	unknown := strings.Replace(normalRecord, "station-test", "unknown", 1)
	future := strings.Replace(normalRecord, "00:00:00Z", "00:02:00Z", 1)
	source := &fakeSource{batch: page(strings.Join([]string{critical, normalRecord, negative, quality, unknown, future, "null"}, ","), `"opaque/+=="`)}
	repo := newMemory()
	p := processor(t, source, repo)
	if err := p.Poll(context.Background(), "mapping"); err != nil {
		t.Fatal(err)
	}
	if len(repo.events) != 2 || len(repo.dlq) != 5 || p.state.Cursor != "opaque/+==" {
		t.Fatalf("events=%d dlq=%d cursor=%s", len(repo.events), len(repo.dlq), p.state.Cursor)
	}
	for _, event := range repo.events {
		want := "NORMAL"
		if event.SourceRefID == "critical-ref" {
			want = "AWAS"
		}
		if event.Source != Source || event.HazardType != "COASTAL" || event.AreaName != "Test Province" || event.Severity != want || event.Latitude != -1 || event.Longitude != 100 {
			t.Fatalf("mapping changed: %+v", event)
		}
	}
	for _, msg := range repo.dlq {
		if !strings.HasPrefix(msg.Reason, "contract_violation:") || msg.Source != Source || msg.Status != deadletter.StatusParked || deadletter.Retryable(msg.Reason) {
			t.Fatalf("incorrect quarantine: %+v", msg)
		}
	}
	if repo.effects[len(repo.effects)-1] != "checkpoint" || !p.state.Status.Stale {
		t.Fatal("checkpoint preceded effects or invalid records appeared fresh")
	}
}

func TestCoastalWriteFailuresNeverAdvanceCursorAndRetryIsIdempotent(t *testing.T) {
	for _, stage := range []string{"dlq", "hazards", "checkpoint", "fence"} {
		t.Run(stage, func(t *testing.T) {
			source := &fakeSource{batch: page(normalRecord+",null", `"next"`)}
			repo := newMemory()
			repo.fail = stage
			p := processor(t, source, repo)
			if err := p.Poll(context.Background(), "failure"); err == nil || p.state.Cursor != "" || repo.cursor != "" {
				t.Fatal("failed write advanced a cursor")
			}
			repo.fail = ""
			if err := p.Poll(context.Background(), "retry"); err != nil {
				t.Fatal(err)
			}
			if source.cursor != "" || len(repo.events) != 1 || len(repo.dlq) != 1 || p.state.Cursor != "next" {
				t.Fatal("retry lost or duplicated effects")
			}
			restarted := processor(t, source, repo)
			if restarted.state.Cursor != "next" {
				t.Fatal("restart lost the durable cursor")
			}
		})
	}
}

func TestCoastalMalformedAndTruncatedDoNotReleaseCursor(t *testing.T) {
	for _, batch := range []pantai.Batch{pantai.DecodeBatch([]byte(`{"broken":`)), {Invalid: &pantai.Violation{Detail: "response_capture_limit"}, Truncated: true}} {
		source := &fakeSource{batch: batch}
		repo := newMemory()
		p := processor(t, source, repo)
		if err := p.Poll(context.Background(), "bad-envelope"); err == nil || p.state.Cursor != "" || repo.cursor != "" {
			t.Fatal("untrustworthy envelope released cursor")
		}
		if batch.Truncated && len(repo.dlq) != 0 || !batch.Truncated && len(repo.dlq) != 1 {
			t.Fatal("truncated archive or missing complete payload")
		}
		for _, msg := range repo.dlq {
			if string(msg.Raw) != string(batch.Raw) {
				t.Fatal("wire payload changed")
			}
		}
	}
}

func TestCoastalCursorLoopIsParkedAndNeverFollowed(t *testing.T) {
	source := &fakeSource{batch: page(normalRecord, `"a"`)}
	repo := newMemory()
	p := processor(t, source, repo)
	if err := p.Poll(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	source.batch = page("", `"b"`)
	if err := p.Poll(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	source.batch = page(normalRecord, `"a"`)
	if err := p.Poll(context.Background(), "loop"); !errors.Is(err, pantai.ErrDataHealth) || p.state.Cursor != "b" || len(repo.dlq) != 1 {
		t.Fatal("cursor cycle was followed or lost")
	}
	for _, msg := range repo.dlq {
		if string(msg.Raw) != string(source.batch.Raw) {
			t.Fatal("cursor metadata quarantine lost envelope bytes")
		}
	}
}

func TestCoastalVirtualClockStallKeepsDataButMarksUnhealthy(t *testing.T) {
	source := &fakeSource{batch: page(normalRecord, `"a"`)}
	repo := newMemory()
	p := processor(t, source, repo)
	clock := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }
	if err := p.Poll(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(11 * time.Second)
	source.batch = page("", `null`)
	if err := p.Poll(context.Background(), "stalled"); !errors.Is(err, pantai.ErrDataHealth) || !p.state.Status.Stale || p.state.Status.Available || len(repo.events) != 1 {
		t.Fatal("virtual-time stall lost history or reported healthy")
	}
	if p.state.GeneratedAt.Year() != 2026 {
		t.Fatal("host clock replaced virtual timestamps")
	}
}

func TestCoastalRecoversFromSensorRestart(t *testing.T) {
	ctx := context.Background()
	repo := newMemory()
	source := &fakeSource{batch: page(normalRecord, `"reused"`)}
	source.batch.GeneratedAt = source.batch.GeneratedAt.Add(72 * time.Hour)
	oldGeneratedAt := source.batch.GeneratedAt
	p := processor(t, source, repo)
	if err := p.Poll(ctx, "initial"); err != nil {
		t.Fatal(err)
	}
	source.batch = page("", `"old-offset"`)
	source.batch.GeneratedAt = oldGeneratedAt
	if err := p.Poll(ctx, "advance"); err != nil {
		t.Fatal(err)
	}
	oldCheckpoint := repo.cursor

	// A new leader can recover using only the old durable checkpoint. The
	// old offset returns a regressed page whose records cannot be trusted.
	p = processor(t, source, repo)
	var requested []string
	restarted := page(normalRecord+",null", `"reused"`)
	source.fetch = func(cursor string) (pantai.Batch, error) {
		requested = append(requested, cursor)
		switch cursor {
		case "old-offset":
			return page(strings.Replace(normalRecord, "independent-ref", "skip-old-offset", 1), `null`), nil
		case "":
			return restarted, nil
		case "reused":
			return page(strings.Replace(normalRecord, "independent-ref", "next-record", 1), `"new-offset"`), nil
		default:
			t.Fatalf("unexpected cursor %q", cursor)
			return pantai.Batch{}, nil
		}
	}
	repo.fail = "checkpoint"
	if err := p.Poll(ctx, "write-fails"); err == nil || repo.cursor != oldCheckpoint ||
		p.state.Cursor != "old-offset" || !p.state.GeneratedAt.Equal(oldGeneratedAt) {
		t.Fatal("failure replaced the old durable or in-memory checkpoint")
	}
	repo.fail = ""
	if err := p.Poll(ctx, "recover"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(requested, []string{"old-offset", "", "old-offset", ""}) ||
		!slices.Equal(p.state.CursorHistory, []string{cursorHash("reused")}) ||
		!p.state.GeneratedAt.Equal(restarted.GeneratedAt) || !p.state.Status.Available ||
		len(repo.events) != 1 || len(repo.dlq) != 1 || p.client.Status().State != "closed" {
		t.Fatalf("recovery failed: requests=%v state=%+v events=%d dlq=%d", requested, p.state, len(repo.events), len(repo.dlq))
	}
	for _, msg := range repo.dlq {
		if msg.Position != "" {
			t.Fatal("replay DLQ item was attributed to the old cursor")
		}
	}
	p = processor(t, source, repo)
	if err := p.Poll(ctx, "resume"); err != nil || source.cursor != "reused" ||
		p.state.Cursor != "new-offset" || len(repo.events) != 2 {
		t.Fatalf("worker did not resume from replacement checkpoint: cursor=%q err=%v", source.cursor, err)
	}
}

func TestCoastalDoesNotResetWithoutValidRegressedHead(t *testing.T) {
	for _, scenario := range []string{"current", "malformed", "upstream error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repo := newMemory()
			source := &fakeSource{batch: page(normalRecord, `"old"`)}
			source.batch.GeneratedAt = source.batch.GeneratedAt.Add(time.Hour)
			p := processor(t, source, repo)
			if err := p.Poll(ctx, "initial"); err != nil {
				t.Fatal(err)
			}
			oldCheckpoint := repo.cursor
			source.fetch = func(cursor string) (pantai.Batch, error) {
				if cursor == "old" {
					return page("", `null`), nil
				}
				switch scenario {
				case "malformed":
					return pantai.DecodeBatch([]byte(`{"broken":`)), nil
				case "upstream error":
					return pantai.Batch{}, &pantai.HTTPError{StatusCode: 401}
				default:
					return source.batch, nil
				}
			}
			if err := p.Poll(ctx, "unconfirmed"); err == nil || repo.cursor != oldCheckpoint ||
				p.state.Cursor != "old" || len(repo.events) != 1 {
				t.Fatalf("unconfirmed restart changed ingest state: %v", err)
			}
		})
	}
}

func TestCoastalLeaseLossPreventsAllEffects(t *testing.T) {
	source := &fakeSource{batch: page(normalRecord, `"a"`)}
	repo := newMemory()
	p := processor(t, source, repo)
	lost := errors.New("lease lost")
	p.verify = func(context.Context) error { return lost }
	if err := p.Poll(context.Background(), "zombie"); !errors.Is(err, lost) || source.calls != 0 || len(repo.effects) != 0 {
		t.Fatal("old leader continued polling or writing")
	}
}

func TestCoastalCheckpointRejectsCorruption(t *testing.T) {
	repo := newMemory()
	repo.cursor = `{"cursor":123}`
	p, err := NewProcessor(repo, &fakeSource{}, 1, func(context.Context) error { return nil }, time.Second, 10, pantai.DefaultResilienceConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Restore(context.Background()); err == nil {
		t.Fatal("corrupt checkpoint silently reset")
	}
	if _, err := json.Marshal(p.state); err != nil {
		t.Fatal(err)
	}
}

func TestCoastalDuplicatesAndResponseOrdering(t *testing.T) {
	for _, scenario := range []string{"identical", "conflicting", "out-of-order"} {
		t.Run(scenario, func(t *testing.T) {
			items := normalRecord + "," + normalRecord
			wantEvents, wantDLQ := 1, 0
			if scenario == "conflicting" {
				items = normalRecord + "," + strings.Replace(normalRecord, `"critical":false`, `"critical":true`, 1)
				wantEvents, wantDLQ = 0, 2
			}
			if scenario == "out-of-order" {
				items = strings.Replace(normalRecord, "independent-ref", "z", 1) + "," + strings.Replace(normalRecord, "independent-ref", "a", 1)
				wantEvents, wantDLQ = 1, 1
			}
			source := &fakeSource{batch: page(items, `"next"`)}
			repo := newMemory()
			p := processor(t, source, repo)
			if err := p.Poll(context.Background(), scenario); err != nil || len(repo.events) != wantEvents || len(repo.dlq) != wantDLQ || p.state.Cursor != "next" {
				t.Fatalf("%s: events=%d dlq=%d err=%v", scenario, len(repo.events), len(repo.dlq), err)
			}
		})
	}
}
