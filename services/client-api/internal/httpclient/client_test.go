package httpclient

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentWavesReuseDependencyConnections(t *testing.T) {
	const concurrency = 16
	var connections atomic.Int64
	var arrivals atomic.Int64
	var release [2]chan struct{}
	for i := range release {
		release[i] = make(chan struct{})
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrival := arrivals.Add(1)
		wave := (arrival - 1) / concurrency
		if arrival%concurrency == 0 {
			close(release[wave])
		}
		select {
		case <-release[wave]:
			_, _ = w.Write([]byte("complete"))
		case <-r.Context().Done():
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	client := New(5 * time.Second)
	defer client.CloseIdleConnections()
	for wave := 0; wave < 2; wave++ {
		var workers sync.WaitGroup
		for i := 0; i < concurrency; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				response, err := client.Get(server.URL)
				if err != nil {
					t.Error(err)
					return
				}
				defer response.Body.Close()
				if _, err := io.Copy(io.Discard, response.Body); err != nil {
					t.Error(err)
				}
			}()
		}
		workers.Wait()
		if got := connections.Load(); got != concurrency {
			t.Fatalf("wave %d opened %d TCP connections; want %d reused across waves", wave, got, concurrency)
		}
	}
}
