package reporting

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
)

type fakeStore struct {
	accepted bool
	err      error
	calls    int
	saved    Report
	corrID   string
}

func (s *fakeStore) SaveReport(_ context.Context, report Report, correlationID string) (bool, error) {
	s.calls++
	s.saved = report
	s.corrID = correlationID
	return s.accepted, s.err
}

type fakeDelivery struct {
	body    []byte
	corrID  string
	count   int
	acked   bool
	nacked  bool
	requeue bool
}

func (d *fakeDelivery) Body() []byte              { return d.body }
func (d *fakeDelivery) CorrelationID() string     { return d.corrID }
func (d *fakeDelivery) DeliveryCount() int        { return d.count }
func (d *fakeDelivery) Ack(context.Context) error { d.acked = true; return nil }
func (d *fakeDelivery) Nack(_ context.Context, requeue bool) error {
	d.nacked = true
	d.requeue = requeue
	return nil
}

const validPayload = `{"report_id":"7f2f2b4a-9c1d-4a2e-8b3f-5e6a7d8c9b0a","hazard_id":"haz_63eca7f7","note":"Banjir rob","area_name":"Pesisir","occurred_at":"2026-11-17T03:30:00Z","reporter":"field-team","received_at":"2026-11-17T03:51:05.893482233Z"}`

func TestParseReportAcceptsValidAndRejectsBad(t *testing.T) {
	report, err := parseReport([]byte(validPayload))
	if err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	if report.ReportID != "7f2f2b4a-9c1d-4a2e-8b3f-5e6a7d8c9b0a" || report.Note != "Banjir rob" ||
		report.Reporter != "field-team" || report.OccurredAt == nil {
		t.Fatalf("parsed fields wrong: %+v", report)
	}
	for _, test := range []struct {
		name    string
		payload string
	}{
		{"empty", ""},
		{"broken json", `{"report_id":`},
		{"missing report_id", `{"note":"x","received_at":"2026-11-17T03:51:05Z"}`},
		{"bad report_id", `{"report_id":"not-uuid","note":"x","received_at":"2026-11-17T03:51:05Z"}`},
		{"missing note", `{"report_id":"7f2f2b4a-9c1d-4a2e-8b3f-5e6a7d8c9b0a","received_at":"2026-11-17T03:51:05Z"}`},
		{"missing received_at", `{"report_id":"7f2f2b4a-9c1d-4a2e-8b3f-5e6a7d8c9b0a","note":"x"}`},
		{"unknown field", `{"report_id":"7f2f2b4a-9c1d-4a2e-8b3f-5e6a7d8c9b0a","note":"x","received_at":"2026-11-17T03:51:05Z","extra":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseReport([]byte(test.payload)); err == nil {
				t.Fatalf("malformed payload accepted: %s", test.payload)
			}
		})
	}
}

func TestHandleCommitsDuplicatesAndRetries(t *testing.T) {
	cases := []struct {
		name      string
		store     fakeStore
		count     int
		wantAck   bool
		wantNack  bool
		wantCalls int
	}{
		{"fresh report commits then acks", fakeStore{accepted: true}, 0, true, false, 1},
		{"duplicate skips effect then acks", fakeStore{accepted: false}, 0, true, false, 1},
		{"store failure redelivers", fakeStore{err: errors.New("db down")}, 0, false, true, 1},
		{"excessive redelivery parks", fakeStore{accepted: true}, maxDeliveryAttempts + 1, true, false, 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			store := test.store
			message := &fakeDelivery{body: []byte(validPayload), corrID: "corr-1", count: test.count}
			got := handle(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), &store, message)
			if got != outcomeAck && !test.wantNack {
				t.Fatalf("unexpected outcome %d", got)
			}
			if message.acked != test.wantAck || message.nacked != test.wantNack {
				t.Fatalf("ack=%v nack=%v, want ack=%v nack=%v", message.acked, message.nacked, test.wantAck, test.wantNack)
			}
			if store.calls != test.wantCalls {
				t.Fatalf("SaveReport calls %d, want %d", store.calls, test.wantCalls)
			}
			if test.wantCalls == 1 && store.corrID != "corr-1" {
				t.Fatal("correlation id not passed to the store")
			}
		})
	}
}

func TestHandleMalformedPayloadParksWithoutStore(t *testing.T) {
	var logs bytes.Buffer
	store := &fakeStore{}
	message := &fakeDelivery{body: []byte(`{broken`), corrID: "corr-2"}
	handle(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), store, message)
	if store.calls != 0 || !message.acked || message.nacked {
		t.Fatalf("malformed payload must park (ack, no store call): calls=%d ack=%v nack=%v",
			store.calls, message.acked, message.nacked)
	}
	if !bytes.Contains(logs.Bytes(), []byte("parked")) {
		t.Fatal("parking is not recorded loudly")
	}
}
