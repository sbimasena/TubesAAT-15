package coastal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/ingest"
)

func TestCoastalFollowerUsesCheckpointWithoutPollingAndExpiresFreshness(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	state := checkpoint{Cursor: "opaque-position", Status: ingest.SourceStatus{Available: true, LastIngested: &old}}
	raw, _ := json.Marshal(state)
	repo := newMemory()
	repo.cursor = string(raw)
	config := DefaultConfig()
	config.BaseURL = server.URL
	config.Token = "test"
	config.PollInterval = time.Millisecond
	r, err := NewRuntime(config, repo, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A short live refresh performs reads only, even with a valid source URL.
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	r.RefreshStatus(ctx, func() bool { return false })
	status := r.Status()
	if requests.Load() != 0 || !status.Stale || status.LastIngested == nil || !status.LastIngested.Equal(old) || status.StaleSince == nil {
		t.Fatalf("follower fabricated freshness or polled: %+v requests=%d", status, requests.Load())
	}
}

func TestCoastalRuntimeCancellationStopsSourceIO(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stations" {
			w.Write([]byte(`{"items":[{"station_id":"station-test","name":"Test Coast","province":"Test Province","latitude":0,"longitude":100}]}`))
			return
		}
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	config := DefaultConfig()
	config.BaseURL, config.Token = server.URL, "temporary-token"
	repo := newMemory()
	r, err := NewRuntime(config, repo, func(ctx context.Context) error { return ctx.Err() }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, 3) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("source request did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("leader cancellation did not stop source I/O")
	}
	if len(repo.effects) != 0 {
		t.Fatal("canceled poll committed effects")
	}
}
