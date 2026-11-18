package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPollVerifiesBeforeEveryUpstreamRequest(t *testing.T) {
	t.Setenv("POD_NAME", "aggregator-one")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Instance-ID") != "aggregator-one" {
			t.Error("missing instance ID")
		}
		w.Write([]byte("[]"))
	}))
	defer server.Close()
	client := NewBMKGClient(server.URL, "key", nil)
	verifications := 0
	ctx := WithLeaseVerifier(context.Background(), func(context.Context) error {
		verifications++
		if verifications > 1 {
			return errors.New("lease lost")
		}
		return nil
	})
	if _, err := client.FetchSeismicEvents(ctx, time.Time{}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchTsunamiWarnings(ctx, time.Time{}, "test"); err == nil {
		t.Fatal("poll after lease loss")
	}
	if calls != 1 || verifications != 2 {
		t.Fatalf("calls=%d verifies=%d", calls, verifications)
	}
}
