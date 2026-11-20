package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
)

const validPVMBGRecord = `{"report_id":"r1","volcano_id":"V-001","alert_level":"WASPADA","reported_at":"2026-11-17T03:51:05Z","confidence_level":0.9,"note":"[},\"still a string"}`

func TestPVMBGBatchIsolatesFailedRecords(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      error
	}{
		{"changed type", strings.Replace(validPVMBGRecord, `"2026-11-17T03:51:05Z"`, `123`, 1), deadletter.ErrSchemaIncompatible},
		{"missing alert", strings.Replace(validPVMBGRecord, `"alert_level":"WASPADA",`, "", 1), deadletter.ErrSchemaIncompatible},
		{"non-object", `null`, deadletter.ErrContractViolation},
		{"deep", `{"x":` + strings.Repeat("[", 20000) + `0` + strings.Repeat("]", 20000) + `}`, deadletter.ErrMessageTooDeep},
		{"invalid UTF-8", "{\"note\":\"\xff\"}", deadletter.ErrInvalidUTF8},
		{"oversize", `{"padding":"` + strings.Repeat("x", pvmbgRecordLimit) + `"}`, deadletter.ErrMessageTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "[" + validPVMBGRecord + ", \n" + tc.raw + ", " + validPVMBGRecord + "]"
			got := parsePVMBGBatch([]byte(raw))
			if got.Invalid != nil || len(got.Records) != 3 {
				t.Fatalf("batch error=%v, records=%d", got.Invalid, len(got.Records))
			}
			bad := got.Records[1]
			if string(bad.Raw) != tc.raw || !errors.Is(bad.Invalid, tc.want) {
				t.Fatalf("failed record bytes preserved=%v, error=%v", string(bad.Raw) == tc.raw, bad.Invalid)
			}
			for _, i := range []int{0, 2} {
				r := got.Records[i]
				if r.Invalid != nil || r.Report.ReportID != "r1" || r.Report.ConfidenceLevel == nil || len(r.Report.Extra["note"]) == 0 {
					t.Fatalf("valid sibling %d rejected or lost additive fields", i)
				}
			}
		})
	}
}

func TestPVMBGBatchPreservesMalformedBody(t *testing.T) {
	for _, raw := range []string{"[" + validPVMBGRecord, `[{},]`, `[{}] trailing`, `[{"x":}]`, `null`} {
		got := parsePVMBGBatch([]byte(raw))
		if !errors.Is(got.Invalid, deadletter.ErrMalformedJSON) || string(got.Raw) != raw || len(got.Records) != 0 {
			t.Fatalf("invalid batch %q not preserved", raw)
		}
	}
}

func TestFetchVolcanicBatchCapsResponseAndPreservesRequest(t *testing.T) {
	since := time.Date(2026, 11, 17, 3, 51, 5, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/volcanic-reports" || r.URL.Query().Get("since") != since.Format(time.RFC3339Nano) ||
			r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Correlation-ID") != "test-poll" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		for range 11 {
			if _, err := io.WriteString(w, strings.Repeat("x", 1<<20)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	got, err := NewPVMBGClient(server.URL, "test-token", nil).FetchVolcanicBatch(context.Background(), since, "test-poll")
	if err != nil || got.Truncated || !errors.Is(got.Invalid, deadletter.ErrMessageTooLarge) || len(got.Raw) != 11<<20 ||
		string(got.Raw) != strings.Repeat("x", 11<<20) {
		t.Fatal(fmt.Errorf("unexpected bounded read: transport=%v payload=%v truncated=%v bytes=%d", err, got.Invalid, got.Truncated, len(got.Raw)))
	}
}

func TestPVMBGCaptureCeilingDoesNotReturnAPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := strings.Repeat("x", 1<<20)
		for range (pvmbgCaptureLimit >> 20) + 1 {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	got, err := NewPVMBGClient(srv.URL, "token", nil).FetchVolcanicBatch(context.Background(), time.Time{}, "test")
	if err != nil || !got.Truncated || !errors.Is(got.Invalid, deadletter.ErrMessageTooLarge) || len(got.Raw) != 0 {
		t.Fatalf("expected an explicit, unarchived overflow: err=%v invalid=%v truncated=%v bytes=%d", err, got.Invalid, got.Truncated, len(got.Raw))
	}
}

func TestDecodePVMBGReplayOnlyRepairsKnownChanges(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(validPVMBGRecord, `"alert_level"`, `"status"`, 1),
		strings.Replace(validPVMBGRecord, `"2026-11-17T03:51:05Z"`, `1794887465`, 1),
	} {
		original := parsePVMBGBatch([]byte("[" + raw + "]"))
		if original.Records[0].Invalid == nil {
			t.Fatal("live decoder silently accepted the schema change")
		}
		got := DecodePVMBGReplay([]byte(raw))
		if got.Invalid != nil || got.Report.AlertLevel == "" || got.Report.ReportedAt.IsZero() {
			t.Fatalf("known migration did not recover: %+v", got)
		}
	}
	missing := strings.Replace(validPVMBGRecord, `"alert_level":"WASPADA",`, "", 1)
	if got := DecodePVMBGReplay([]byte(missing)); !errors.Is(got.Invalid, deadletter.ErrSchemaIncompatible) {
		t.Fatal("replay cannot invent missing information")
	}
}

func TestFetchVolcanicBatchSeparatesUpstreamFailureFromMalformedPayload(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const raw = `[{"report_id":`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, raw)
			}))
			defer server.Close()

			batch, err := NewPVMBGClient(server.URL, "test-token", nil).FetchVolcanicBatch(context.Background(), time.Time{}, "test-poll")
			if status == http.StatusServiceUnavailable {
				if err == nil || batch.Invalid != nil || len(batch.Raw) != 0 {
					t.Fatalf("HTTP failure should not enter payload quarantine: batch=%+v err=%v", batch, err)
				}
			} else if err != nil || !errors.Is(batch.Invalid, deadletter.ErrMalformedJSON) || string(batch.Raw) != raw {
				t.Fatalf("malformed successful body should be preserved: batch=%+v err=%v", batch, err)
			}
		})
	}
}
