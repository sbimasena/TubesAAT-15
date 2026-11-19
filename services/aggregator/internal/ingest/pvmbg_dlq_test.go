package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
)

type fakePVMBGDLQ struct {
	messages []deadletter.Message
	epoch    int64
	err      error
}

func (f *fakePVMBGDLQ) RecordPVMBG(_ context.Context, epoch int64, msg deadletter.Message) error {
	f.messages = append(f.messages, msg)
	f.epoch = epoch
	return f.err
}

func TestPVMBGCheckpointRequiresDLQAndCanonicalCommit(t *testing.T) {
	const invalid = `{"report_id":"bad", "volcano_id":"V-001","reported_at":123}`
	const valid = `{"report_id":"good","volcano_id":"V-001","reported_at":"2026-11-19T00:00:00Z","alert_level":"SIAGA"}`
	wantCursor := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "["+invalid+","+valid+"]")
	}))
	defer server.Close()
	ctx := context.Background()
	dlq := &fakePVMBGDLQ{err: errors.New("DLQ unavailable")}
	repo := &retryRepository{}
	cursors := &checkpoints{saved: map[string]string{}}
	m := NewManager(nil, source.NewPVMBGClient(server.URL, "token", nil), repo, time.Second, nil)
	m.pvmbgDLQ = dlq
	if err := m.PrepareTerm(ctx, 7, func(context.Context) error { return nil }, cursors); err != nil {
		t.Fatal(err)
	}
	assertHeld := func() {
		t.Helper()
		if !m.reportCursor.IsZero() || cursors.saved["PVMBG"] != "" {
			t.Fatal("checkpoint advanced without all required commits")
		}
	}

	m.pollPVMBG(ctx, "dlq-down")
	assertHeld()
	if len(repo.events) != 1 || len(dlq.messages) != 1 || dlq.epoch != 7 {
		t.Fatal("valid sibling was not saved or the fenced DLQ write was not attempted")
	}
	msg := dlq.messages[0]
	if string(msg.Raw) != invalid || msg.Source != "PVMBG" ||
		msg.Status != deadletter.StatusParked || msg.Reason != deadletter.ReasonSchemaIncompatible {
		t.Fatalf("unexpected DLQ message: %+v", msg)
	}

	dlq.err, repo.fail = nil, true
	m.pollPVMBG(ctx, "canonical-down")
	assertHeld()

	repo.fail, cursors.fail = false, true
	m.pollPVMBG(ctx, "checkpoint-down")
	assertHeld()

	cursors.fail = false
	m.pollPVMBG(ctx, "recovered")
	var saved checkpoint
	if err := json.Unmarshal([]byte(cursors.saved["PVMBG"]), &saved); err != nil {
		t.Fatal(err)
	}
	if !m.reportCursor.Equal(wantCursor) || !saved.ReportCursor.Equal(wantCursor) || cursors.epoch != 7 || len(repo.events) != 1 {
		t.Fatal("expected one canonical event and a committed checkpoint after recovery")
	}
	for _, attempt := range dlq.messages {
		if attempt.MessageID != msg.MessageID || msg.MessageID == "" {
			t.Fatal("retry changed the DLQ idempotency key")
		}
	}
}

func TestPVMBGDoesNotAdvanceOnMalformedEnvelopeOrHTTPFailure(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const raw = " \n[{\"report_id\":"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, raw)
			}))
			defer server.Close()
			ctx := context.Background()
			cursors := &checkpoints{saved: map[string]string{}}
			dlq := &fakePVMBGDLQ{}
			m := NewManager(nil, source.NewPVMBGClient(server.URL, "token", nil), &retryRepository{}, time.Second, nil)
			m.pvmbgDLQ = dlq
			if err := m.PrepareTerm(ctx, 7, func(context.Context) error { return nil }, cursors); err != nil {
				t.Fatal(err)
			}
			m.pollPVMBG(ctx, "bad-response")
			if !m.reportCursor.IsZero() || cursors.saved["PVMBG"] != "" {
				t.Fatal("unreadable response advanced the checkpoint")
			}
			if status == http.StatusOK {
				if len(dlq.messages) != 1 || string(dlq.messages[0].Raw) != raw || dlq.messages[0].Reason != deadletter.ReasonMalformedJSON {
					t.Fatal("malformed body should be preserved verbatim")
				}
			} else if len(dlq.messages) != 0 {
				t.Fatal("HTTP 503 must not park a payload")
			}
		})
	}
}
