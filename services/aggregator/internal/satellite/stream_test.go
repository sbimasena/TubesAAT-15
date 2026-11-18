package satellite

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSSEPreservesDataAndSkipsCommittedOffsets(t *testing.T) {
	raw := ": comment\r\nevent: detection\r\nid: 7\r\ndata: duplicate\r\n\r\nevent: update\nid: 8\ndata: {\ndata: \"latitude\":-2.51230,\ndata: \"acq_time\":243}\n\nevent: heartbeat\ndata: {}\n\nevent: retract\nid: 9\ndata: incomplete"
	var frames []Frame
	err := ReadSSE(context.Background(), strings.NewReader(raw), 7, func(_ context.Context, f Frame) error { frames = append(frames, f); return nil })
	if err == nil || len(frames) != 2 {
		t.Fatalf("frames=%d err=%v", len(frames), err)
	}
	if frames[0].Offset != 8 || string(frames[0].Data) != "{\n\"latitude\":-2.51230,\n\"acq_time\":243}" || frames[1].Offset != -1 {
		t.Fatalf("%+v", frames)
	}
}

func TestSSERejectsGapsAndStopsAtFailedCommit(t *testing.T) {
	for _, raw := range []string{"event: detection\nid: 4\ndata: {}\n\n", "event: detection\ndata: {}\n\n", "event: detection\nid: x\ndata: {}\n\n"} {
		err := ReadSSE(context.Background(), strings.NewReader(raw), 2, func(context.Context, Frame) error { t.Fatal("invalid frame applied"); return nil })
		if !errors.Is(err, ErrProtocol) {
			t.Fatal(err)
		}
	}
	failure := errors.New("commit failed")
	calls := 0
	err := ReadSSE(context.Background(), strings.NewReader("event: detection\nid: 3\ndata: {}\n\nevent: detection\nid: 4\ndata: {}\n\n"), 2, func(context.Context, Frame) error { calls++; return failure })
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestStreamResumesFromDurableOffset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var requests atomic.Int32
	var committed atomic.Int64
	committed.Store(-1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing token")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		n := requests.Add(1)
		if n == 1 {
			fmt.Fprint(w, "event: detection\nid: 1\ndata: {}\n\n")
			return
		}
		if r.URL.Query().Get("from") != "1" || r.Header.Get("Last-Event-ID") != "1" {
			t.Error("not resumed from committed position")
		}
		fmt.Fprint(w, "event: detection\nid: 1\ndata: {}\n\nevent: update\nid: 2\ndata: {}\n\n")
	}))
	defer server.Close()
	client := &StreamClient{base: server.URL, token: "test-token", http: server.Client(), idle: time.Second}
	applied := 0
	err := client.Run(ctx, func(context.Context) (int64, error) { return committed.Load(), nil }, func(_ context.Context, frames []Frame) error {
		f := frames[0]
		applied++
		committed.Store(f.Offset)
		if f.Offset == 2 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || applied != 2 || committed.Load() != 2 {
		t.Fatalf("applied=%d offset=%d err=%v", applied, committed.Load(), err)
	}
}

func TestIdleStreamIsCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := &StreamClient{base: server.URL, token: "token", http: server.Client(), idle: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if err := client.once(ctx, -1, func(context.Context, []Frame) error { return nil }); err == nil || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("idle stream did not stop: %v", err)
	}
}

func TestStreamDoesNotRetryCommitFailureOrUnauthorized(t *testing.T) {
	for _, status := range []int{200, 401} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(status)
				fmt.Fprint(w, "event: detection\nid: 1\ndata: {}\n\n")
			}))
			defer server.Close()
			client := &StreamClient{base: server.URL, token: "token", http: server.Client(), idle: time.Second}
			failure := errors.New("store unavailable")
			err := client.Run(context.Background(), func(context.Context) (int64, error) { return -1, nil }, func(context.Context, []Frame) error { return failure })
			if err == nil || calls.Load() != 1 {
				t.Fatalf("calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestSessionValidatesRequiredProgressFields(t *testing.T) {
	for _, body := range []string{`{"latest_offset":12,"virtual_now":"2026-11-12T00:00:00Z"}`, `{"latest_offset":12}`, `{"latest_offset":-1,"virtual_now":"2026-11-12T00:00:00Z"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer token" {
				t.Error("missing session auth")
			}
			fmt.Fprint(w, body)
		}))
		client := &StreamClient{base: server.URL, token: "token", http: server.Client()}
		value, err := client.Session(context.Background())
		if strings.Contains(body, `"latest_offset":12,"virtual_now"`) {
			if err != nil || value.LatestOffset != 12 {
				t.Fatal(value, err)
			}
		} else if err == nil {
			t.Fatal("invalid session accepted")
		}
		server.Close()
	}
}
