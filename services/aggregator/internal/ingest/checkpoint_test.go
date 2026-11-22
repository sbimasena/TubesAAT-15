package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

type checkpoints struct {
	saved map[string]string
	fail  bool
	epoch int64
}

func (c *checkpoints) LoadCursor(_ context.Context, source string) (string, error) {
	return c.saved[source], nil
}
func (c *checkpoints) SaveCursor(_ context.Context, epoch int64, source, value string) error {
	if c.fail {
		return errors.New("cursor unavailable")
	}
	c.epoch = epoch
	c.saved[source] = value
	return nil
}
func TestCheckpointRestoresCorrelationAndEpoch(t *testing.T) {
	ctx := context.Background()
	c := &checkpoints{saved: map[string]string{}}
	m := NewManager(nil, nil, &retryRepository{}, time.Second, nil)
	if err := m.PrepareTerm(ctx, 9, func(context.Context) error { return nil }, c); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := checkpoint{EventCursor: now, Events: map[string]domain.SeismicEvent{"old": {EventID: "old"}}, Warnings: map[string]domain.TsunamiWarning{"old": {EventID: "old", ThreatLevel: "AWAS"}}}
	if err := m.saveCheckpoint(ctx, "BMKG", state); err != nil {
		t.Fatal(err)
	}
	if c.epoch != 9 {
		t.Fatalf("epoch=%d", c.epoch)
	}
	recovered := NewManager(nil, nil, &retryRepository{}, time.Second, nil)
	if err := recovered.PrepareTerm(ctx, 10, func(context.Context) error { return nil }, c); err != nil {
		t.Fatal(err)
	}
	if !recovered.eventCursor.Equal(now) || recovered.eventsByID["old"].EventID != "old" || recovered.warningsByEvent["old"].ThreatLevel != "AWAS" {
		t.Fatal("checkpoint lost correlation state")
	}
	c.fail = true
	if recovered.saveCheckpoint(ctx, "BMKG", checkpoint{}) == nil {
		t.Fatal("cursor error hidden")
	}
	recovered.verify = func(context.Context) error { return errors.New("lease lost") }
	if recovered.persist(ctx, "BMKG", "test", nil) == nil {
		t.Fatal("write after lease loss")
	}
}

func TestFollowersReadDurableFreshnessWithoutPolling(t *testing.T) {
	now := time.Now().UTC()
	state := checkpoint{Status: &SourceStatus{Available: true, LastSuccess: &now, LastIngested: &now}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	c := &checkpoints{saved: map[string]string{"BMKG": string(raw), "PVMBG": string(raw)}}
	// No upstream clients are configured: any attempted poll would panic.
	m := NewManager(nil, nil, &retryRepository{}, time.Millisecond, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	m.RunStatusRefresh(ctx, c, func() bool { return false })
	for name, status := range m.Status() {
		if !status.Available || status.Stale {
			t.Fatalf("%s: %+v", name, status)
		}
	}
}

func TestCheckpointKeepsOnlyRecentCorrelation(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	events := map[string]domain.SeismicEvent{
		"recent": {EventID: "recent", OccurredAt: now.Add(-time.Minute)},
		"old":    {EventID: "old", OccurredAt: now.Add(-2 * checkpointWindow)},
	}
	warnings := map[string]domain.TsunamiWarning{
		"recent":   {EventID: "recent"},
		"old":      {EventID: "old"},
		"unmapped": {EventID: "unmapped"},
	}
	keptEvents, keptWarnings := recentCorrelation(events, warnings, now)
	if len(keptEvents) != 1 || keptEvents["recent"].EventID != "recent" {
		t.Fatalf("events: %v", keptEvents)
	}
	if _, ok := keptWarnings["old"]; ok || len(keptWarnings) != 2 || keptWarnings["unmapped"].EventID != "unmapped" {
		t.Fatalf("warnings: %v", keptWarnings)
	}
}

func TestPrepareTermCapsFutureCheckpointCursors(t *testing.T) {
	ctx := context.Background()
	future := time.Now().UTC().Add(5 * time.Minute)
	raw, _ := json.Marshal(checkpoint{EventCursor: future, WarningCursor: future})
	c := &checkpoints{saved: map[string]string{"BMKG": string(raw)}}
	m := NewManager(nil, nil, &retryRepository{}, time.Second, nil)
	if err := m.PrepareTerm(ctx, 2, func(context.Context) error { return nil }, c); err != nil {
		t.Fatal(err)
	}
	if now := time.Now().UTC(); m.eventCursor.IsZero() || m.eventCursor.After(now) || m.warningCursor.After(now) {
		t.Fatalf("restored future cursors: event=%s warning=%s", m.eventCursor, m.warningCursor)
	}
}
