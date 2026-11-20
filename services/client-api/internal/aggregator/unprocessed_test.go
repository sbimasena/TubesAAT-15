package aggregator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestReplayReroutesOnlyOnExplicitFollowerResponse(t *testing.T) {
	for _, follower := range []bool{true, false} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || !r.Close {
				t.Error("replay must open a fresh connection per attempt")
			}
			if calls.Add(1) == 1 {
				if follower {
					w.Header().Set("Retry-After", "1")
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		}))
		client, err := NewClient(server.URL, time.Second, nil)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		client.SetAuditToken("audit")
		err = client.ReplayPVMBG(context.Background(), []byte(`{"message_ids":["example"]}`), "replay-test")
		server.Close()
		if follower && (err != nil || calls.Load() != 2) {
			t.Fatalf("follower was not rerouted: calls=%d err=%v", calls.Load(), err)
		}
		if !follower && (err == nil || calls.Load() != 1) {
			t.Fatalf("unexpected retry after a possible write: calls=%d err=%v", calls.Load(), err)
		}
	}
}
