package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
)

func TestUnprocessedInternalOpsOnlyAndMetadataOnly(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/internal/audit/unprocessed" || r.URL.Query().Get("source") != "PVMBG" ||
			r.Header.Get("Authorization") != "Bearer service-audit" || r.Header.Get("X-Correlation-ID") != "audit-test" {
			t.Errorf("unexpected upstream audit request")
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"items":[{"message_id":"pvmbg:123","status":"parked","raw":"private"}],"next_cursor":null}`)
	}))
	defer upstream.Close()
	client, err := aggregator.NewClient(upstream.URL, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	client.SetAuditToken("service-audit")
	for _, tc := range []struct {
		scope, token, query string
		status              int
	}{
		{"internal-ops", "", "?source=PVMBG", 401},
		{"media", "test-access", "?source=PVMBG", 403},
		{"field-team", "test-access", "?source=PVMBG", 403},
		{"internal-ops", "test-access", "?source=PVMBG&source=BMKG", 400},
		{"internal-ops", "test-access", "?source=PVMBG", 200},
	} {
		before := calls.Load()
		h := NewHandler(client, &testValidator{scope: tc.scope}, false, 64, DefaultPools(), nil, nil)
		r := httptest.NewRequest(http.MethodGet, "/internal/audit/unprocessed"+tc.query, nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		r.Header.Set("X-Correlation-ID", "audit-test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("scope=%s want=%d got=%d body=%s", tc.scope, tc.status, w.Code, w.Body.String())
		}
		if tc.status != 200 && calls.Load() != before {
			t.Fatal("rejected request reached Aggregator")
		}
		if tc.status == 200 && (!strings.Contains(w.Body.String(), "pvmbg:123") || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), `"raw"`)) {
			t.Fatalf("bad metadata projection: %s", w.Body.String())
		}
	}
}
