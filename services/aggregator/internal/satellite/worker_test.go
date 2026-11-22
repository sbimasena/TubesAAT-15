package satellite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

func TestWorkerSSEReplayThroughPostgres(t *testing.T) {
	router := postgresRouter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	const first = `{"satellite":"N20","acq_date":"2026-11-12","acq_time":243,"latitude":-2.51230,"longitude":113.80000,"confidence":"n","frp":10.0}`
	const second = `{"satellite":"N21","acq_date":"2026-11-12","acq_time":"0244","latitude":"-2.51230","longitude":"113.80100","confidence":"h","frp":8.0}`
	const key = "N20|2026-11-12|243|-2.51230|113.80000"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("expected fixture authentication")
		}
		if req.URL.Path == "/v1/session" {
			fmt.Fprint(w, `{"latest_offset":5,"virtual_now":"2026-11-12T05:00:00Z"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		n := requests.Add(1)
		if n == 1 {
			fmt.Fprintf(w, "id: 1\nevent: detection\ndata: %s\n\nid: 2\nevent: detection\ndata: %s\n\n", first, second)
			return
		}
		if n == 2 {
			if req.URL.Query().Get("from") != "2" || req.Header.Get("Last-Event-ID") != "2" {
				t.Error("worker did not resume from checkpoint 2")
			}
			page, err := router.Query(req.Context(), store.Filter{Source: "SATELIT"}, "")
			if err != nil || len(page.Items) != 1 {
				t.Errorf("expected one materialized fire before reconnect: %+v, %v", page.Items, err)
			} else if keys, err := eventKeys(page.Items[0]); err != nil || keys[0] != key {
				t.Errorf("detection key was altered: %v, %v", keys, err)
			}
			fmt.Fprintf(w, "id: 2\nevent: detection\ndata: %s\n\nid: 3\nevent: heartbeat\ndata: {}\n\nid: 4\nevent: retract\ndata: {\"key\":%q}\n\n", second, key)
			return
		}
		if req.URL.Query().Get("from") != "4" {
			t.Error("worker did not resume from checkpoint 4")
		}
		fmt.Fprint(w, "id: 5\nevent: update\ndata: {\"unexpected\":true}\n\n")
	}))
	defer server.Close()
	client := &StreamClient{base: server.URL, token: "fixture-token", http: server.Client(), idle: time.Second}
	err := RunWorker(ctx, client, router, 1, func(ctx context.Context) error { return ctx.Err() }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, ErrProtocol) || requests.Load() != 3 {
		t.Fatalf("unexpected worker exit after %d requests: %v", requests.Load(), err)
	}
	status := StoredStatus(ctx, router).(map[string]any)
	if status["durable_offset"] != int64(4) {
		t.Fatalf("invalid update advanced the offset: %v", status)
	}
	result, err := router.Checksum(ctx, "SATELIT")
	if err != nil || result.Count != 0 {
		t.Fatalf("withdrawn fire remained active: %+v, %v", result, err)
	}
}

func TestWorkerSyntheticBurstCatchUp(t *testing.T) {
	router := postgresRouter(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	const burstSize, detectionsPerFire = 1000, 20
	dates := []string{"2026-11-12", "2026-11-14"} // Cross the default 36-hour pruning horizon.
	end := make(chan time.Time, len(dates))
	nextBurst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/session":
			fmt.Fprint(w, `{"latest_offset":2000,"virtual_now":"2026-11-14T04:00:00Z"}`)
		case "/v1/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			for burst, date := range dates {
				for i := range burstSize {
					offset := burst*burstSize + i + 1
					lon := 100 + float64(i/detectionsPerFire)*.03 + float64(i%detectionsPerFire)*.00002
					fmt.Fprintf(w, "id: %d\nevent: detection\ndata: {\"satellite\":\"N20\",\"acq_date\":\"%s\",\"acq_time\":243,\"latitude\":-2.51230,\"longitude\":%.6f,\"confidence\":\"n\",\"frp\":3}\n\n", offset, date, lon)
				}
				w.(http.Flusher).Flush()
				end <- time.Now()
				select {
				case <-nextBurst:
				case <-req.Context().Done():
					return
				}
			}
		}
	}))
	defer server.Close()
	defer cancel()
	client := &StreamClient{base: server.URL, token: "fixture-token", http: server.Client(), idle: time.Minute}
	done := make(chan error, 1)
	go func() {
		done <- RunWorker(ctx, client, router, 1, func(ctx context.Context) error { return ctx.Err() }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	keys := make(map[string]bool)
	for burst := range dates {
		var sent time.Time
		select {
		case sent = <-end:
		case err := <-done:
			t.Fatalf("worker exited before burst %d: %v", burst+1, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		wantOffset := int64((burst + 1) * burstSize)
	poll:
		for {
			select {
			case err := <-done:
				t.Fatalf("worker exited during burst %d: %v", burst+1, err)
			case <-ctx.Done():
				t.Fatalf("burst %d did not converge: %v", burst+1, ctx.Err())
			case <-ticker.C:
				status := StoredStatus(ctx, router).(map[string]any)
				if offset, ok := status["processed_offset"].(*int64); ok && offset != nil && *offset == wantOffset {
					break poll
				}
			}
		}
		result, err := router.Checksum(ctx, "SATELIT")
		if err != nil || result.Partial || result.Count != int(wantOffset)/detectionsPerFire {
			t.Fatalf("unexpected fire count after burst %d: %+v, %v", burst+1, result, err)
		}
		t.Logf("burst %d materialized through offset %d in %s after flush", burst+1, wantOffset, time.Since(sent).Round(time.Millisecond))
		if burst+1 < len(dates) {
			nextBurst <- struct{}{}
		}
	}
	page, err := router.Query(ctx, store.Filter{Source: "SATELIT", Limit: 100}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Items {
		members, err := eventKeys(event)
		if err != nil || len(members) != detectionsPerFire {
			t.Fatalf("incorrect fire size: %d, %v", len(members), err)
		}
		for _, key := range members {
			if keys[key] {
				t.Fatalf("duplicate detection %s", key)
			}
			keys[key] = true
		}
	}
	for _, date := range dates {
		for i := range burstSize {
			lon := 100 + float64(i/detectionsPerFire)*.03 + float64(i%detectionsPerFire)*.00002
			key := fmt.Sprintf("N20|%s|243|-2.51230|%.6f", date, lon)
			if !keys[key] {
				t.Fatalf("missing detection %s", key)
			}
		}
	}
	raw, err := router.LoadCursor(ctx, "SATELIT")
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint recoveryCheckpoint
	if err := json.Unmarshal([]byte(raw), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := applyDelta(ctx, router, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if got := len(checkpoint.State.Detections); got != burstSize {
		t.Fatalf("checkpoint retained %d detections after pruning, want %d", got, burstSize)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
