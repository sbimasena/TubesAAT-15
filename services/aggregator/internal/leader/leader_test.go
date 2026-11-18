package leader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerifyRejectsZombieAndAPIFailure(t *testing.T) {
	holder, epoch := "pod-new", int32(8)
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		value := lease{}
		value.Spec.Holder = holder
		value.Spec.Epoch = int64(epoch)
		json.NewEncoder(w).Encode(value)
	}))
	defer server.Close()
	l := &Leader{client: server.Client(), url: server.URL, identity: "pod-old"}
	l.set(7, true)
	if l.Verify(context.Background()) == nil {
		t.Fatal("zombie accepted")
	}
	holder = "pod-old"
	if l.Verify(context.Background()) == nil {
		t.Fatal("old epoch accepted")
	}
	epoch = 7
	if err := l.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = http.StatusForbidden
	if l.Verify(context.Background()) == nil {
		t.Fatal("API failure accepted")
	}
	l.set(7, false)
	if l.Verify(context.Background()) == nil {
		t.Fatal("inactive term accepted")
	}
}

func TestStandaloneTermStopsOnCancellation(t *testing.T) {
	l := &Leader{}
	ctx, cancel := context.WithCancel(context.Background())
	err := l.Run(ctx, func(ctx context.Context, epoch int64) {
		if epoch != 0 {
			t.Fatal(epoch)
		}
		if err := l.Verify(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
		<-ctx.Done()
	}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if _, active := l.Current(); active {
		t.Fatal("term still active")
	}
}

// A fake API server enforces resourceVersion CAS exactly where contenders race.
func TestContendersHaveOneTermAndCancelWorkers(t *testing.T) {
	var mu sync.Mutex
	var current lease
	version := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if version == 0 {
				w.WriteHeader(404)
				return
			}
		case http.MethodPost, http.MethodPut:
			var incoming lease
			if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if (r.Method == http.MethodPost && version != 0) || (r.Method == http.MethodPut && incoming.Metadata.ResourceVersion != strconv.Itoa(version)) {
				w.WriteHeader(409)
				return
			}
			version++
			incoming.Metadata.ResourceVersion = strconv.Itoa(version)
			current = incoming
			if r.Method == http.MethodPost {
				w.WriteHeader(201)
			}
		default:
			w.WriteHeader(405)
			return
		}
		json.NewEncoder(w).Encode(current)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var workers atomic.Int32
	var calls atomic.Int32
	started := make(chan struct{}, 3)
	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		l := &Leader{client: server.Client(), url: server.URL, identity: strconv.Itoa(i)}
		go func() {
			done <- l.Run(ctx, func(term context.Context, epoch int64) {
				calls.Add(1)
				if epoch != 1 {
					t.Errorf("epoch=%d", epoch)
				}
				workers.Add(1)
				started <- struct{}{}
				<-term.Done()
				workers.Add(-1)
			}, func() {})
		}()
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("no winner")
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not stop")
		}
	}
	if calls.Load() != 1 || workers.Load() != 0 {
		t.Fatalf("starts=%d active=%d", calls.Load(), workers.Load())
	}
}
