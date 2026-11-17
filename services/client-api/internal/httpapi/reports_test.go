package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/authclient"
	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/reportqueue"
)

type fakeSink struct {
	calls  int
	report reportqueue.Report
	corrID string
	err    error
}

func (s *fakeSink) Publish(_ context.Context, report reportqueue.Report, correlationID string) error {
	s.calls++
	s.report = report
	s.corrID = correlationID
	return s.err
}

func reportRequest(t *testing.T, body string, idempotencyKey string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/field-reports", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer test-access")
	r.Header.Set("X-Correlation-ID", "report-test")
	if idempotencyKey != "" {
		r.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return r
}

const validReportBody = `{"report_id":"3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c","hazard_id":"haz_abc123","note":"Banjir rob naik 40cm","area_name":"Pesisir Utara","occurred_at":"2026-11-17T08:00:00Z"}`

func TestFieldReportsAcceptsFieldTeamAfterConfirm(t *testing.T) {
	sink := &fakeSink{}
	handler := NewHandler(nil, &testValidator{scope: "field-team"}, false, 64, DefaultPools(), sink, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reportRequest(t, validReportBody, ""))
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 after confirm, got %d: %s", w.Code, w.Body.String())
	}
	if sink.calls != 1 {
		t.Fatalf("expected exactly one publish, got %d", sink.calls)
	}
	var response struct {
		ReportID string `json:"report_id"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.ReportID != "3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c" || response.Status != "accepted" {
		t.Fatalf("unexpected 202 body: %s", w.Body.String())
	}
	if sink.report.Reporter != "field-team" || sink.report.Note != "Banjir rob naik 40cm" ||
		sink.report.HazardID != "haz_abc123" || sink.report.AreaName != "Pesisir Utara" ||
		sink.report.OccurredAt == nil || sink.corrID != "report-test" {
		t.Fatalf("report not propagated correctly: %+v corr=%q", sink.report, sink.corrID)
	}
}

func TestFieldReportsScopeAndAuthEnforced(t *testing.T) {
	for _, test := range []struct {
		name     string
		scope    string
		validate error
		want     int
	}{
		{"media forbidden", "media", nil, http.StatusForbidden},
		{"internal ops forbidden", "internal-ops", nil, http.StatusForbidden},
		{"invalid token", "field-team", authclient.ErrInvalid, http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &fakeSink{}
			validator := &testValidator{scope: test.scope, err: test.validate}
			handler := NewHandler(nil, validator, false, 64, DefaultPools(), sink, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, reportRequest(t, validReportBody, ""))
			if w.Code != test.want {
				t.Fatalf("expected %d, got %d", test.want, w.Code)
			}
			if sink.calls != 0 {
				t.Fatalf("publish attempted despite rejection: %d calls", sink.calls)
			}
		})
	}
}

func TestFieldReportsValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		key  string
		code string
	}{
		{"missing note", `{"report_id":"3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c"}`, "", "invalid_note"},
		{"blank note", `{"report_id":"3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c","note":"   "}`, "", "invalid_note"},
		{"bad uuid", `{"report_id":"not-a-uuid","note":"x"}`, "", "invalid_report_id"},
		{"missing key", `{"note":"x"}`, "", "invalid_report_id"},
		{"key conflict", `{"report_id":"3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c","note":"x"}`, "different-key", "idempotency_conflict"},
		{"bad occurred_at", `{"report_id":"3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c","note":"x","occurred_at":"yesterday"}`, "", "invalid_occurred_at"},
		{"bad hazard id", `{"report_id":"3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c","note":"x","hazard_id":"bad id!"}`, "", "invalid_hazard_id"},
		{"unknown field", `{"report_id":"3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c","note":"x","extra":1}`, "", "invalid_body"},
		{"oversize note", `{"report_id":"3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c","note":"` + string(bytes.Repeat([]byte("x"), 5000)) + `"}`, "", "invalid_note"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			sink := &fakeSink{}
			handler := NewHandler(nil, &testValidator{scope: "field-team"}, false, 64, DefaultPools(), sink, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, reportRequest(t, test.body, test.key))
			assertAPIError(t, w, http.StatusBadRequest, test.code, "report-test")
			if sink.calls != 0 {
				t.Fatal("invalid report reached the sink")
			}
		})
	}
}

func TestFieldReportsIdempotencyHeaderFallback(t *testing.T) {
	sink := &fakeSink{}
	handler := NewHandler(nil, &testValidator{scope: "field-team"}, false, 64, DefaultPools(), sink, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reportRequest(t, `{"note":"kerusakan tanggul"}`, "3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c"))
	if w.Code != http.StatusAccepted || sink.calls != 1 ||
		sink.report.ReportID != "3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c" {
		t.Fatalf("header fallback failed: %d %s calls=%d", w.Code, w.Body.String(), sink.calls)
	}
}

func TestFieldReportsBrokerFailureIs503Not202(t *testing.T) {
	// D-FR: a report without a durable confirm must never be answered 202.
	sink := &fakeSink{err: reportqueue.ErrUnavailable}
	handler := NewHandler(nil, &testValidator{scope: "field-team"}, false, 64, DefaultPools(), sink, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, reportRequest(t, validReportBody, ""))
	assertAPIError(t, w, http.StatusServiceUnavailable, "broker_unavailable", "report-test")
}
