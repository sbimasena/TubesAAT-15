package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

func TestPVMBGPostgresCheckpointWaitsForDeadLetter(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL for the PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := pgx.Identifier{fmt.Sprintf("ingest_test_%d", time.Now().UnixNano())}.Sanitize()
	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	defer conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	repo, err := store.OpenRouter(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	repo.CheckUnits(ctx)
	if err := repo.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	const epoch = 3
	if err := repo.Fence(ctx, epoch); err != nil {
		t.Fatal(err)
	}
	const bad = `{"report_id":"bad", "volcano_id":"V-001","reported_at":"2026-11-19T00:00:01Z","status":"SIAGA"}`
	const good = `{"report_id":"good","volcano_id":"V-001","reported_at":"2026-11-19T00:00:00Z","alert_level":"SIAGA"}`
	large := `[{"padding":"` + strings.Repeat("x", 11<<20) + `"}]`
	var sendLarge atomic.Bool
	var sendTransient atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sendLarge.Load() {
			_, _ = io.WriteString(w, large)
			return
		}
		if sendTransient.Load() {
			_, _ = io.WriteString(w, `[{"report_id":"transient","volcano_id":"V-001","reported_at":"2026-11-19T01:00:00Z","alert_level":"SIAGA"}]`)
			return
		}
		_, _ = io.WriteString(w, "["+bad+","+good+"]")
	}))
	defer server.Close()
	m := NewManager(nil, source.NewPVMBGClient(server.URL, "test-token", nil), repo, time.Second, nil)
	if err := m.PrepareTerm(ctx, epoch, func(context.Context) error { return nil }, repo); err != nil {
		t.Fatal(err)
	}
	// Fail only DLQ inserts, leaving the canonical and checkpoint paths usable.
	exec("ALTER TABLE " + schema + ".unprocessed_messages ADD CONSTRAINT test_reject_pvmbg CHECK (source <> 'PVMBG')")
	m.pollPVMBG(ctx, "dlq-rejected")
	cursor, err := repo.LoadCursor(ctx, "PVMBG")
	if err != nil || cursor != "" || !m.reportCursor.IsZero() {
		t.Fatalf("checkpoint must stay put: durable=%q local=%s err=%v", cursor, m.reportCursor, err)
	}
	assertCount := func(table string, want int) {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+schema+"."+table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s rows=%d, want %d: %v", table, n, want, err)
		}
	}
	assertCount("hazard_events", 1)
	assertCount("unprocessed_messages", 0)

	exec("ALTER TABLE " + schema + ".unprocessed_messages DROP CONSTRAINT test_reject_pvmbg")
	m.pollPVMBG(ctx, "dlq-recovered")
	m.pollPVMBG(ctx, "overlap-retry")
	assertCount("hazard_events", 1)
	assertCount("unprocessed_messages", 1)
	var raw []byte
	var status, reason string
	err = conn.QueryRow(ctx, "SELECT raw, status, reason FROM "+schema+".unprocessed_messages WHERE source='PVMBG'").
		Scan(&raw, &status, &reason)
	if err != nil || string(raw) != bad || status != "parked" || reason != "schema_incompatible" {
		t.Fatalf("wrong DLQ record: bytesMatch=%v status=%q reason=%q err=%v", string(raw) == bad, status, reason, err)
	}
	cursor, err = repo.LoadCursor(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	var saved checkpoint
	want := time.Date(2026, 11, 19, 0, 0, 1, 0, time.UTC)
	if err := json.Unmarshal([]byte(cursor), &saved); err != nil || !saved.ReportCursor.Equal(want) || !m.reportCursor.Equal(want) {
		t.Fatalf("checkpoint did not advance after recovery: durable=%q local=%s err=%v", cursor, m.reportCursor, err)
	}
	var id string
	if err := conn.QueryRow(ctx, "SELECT message_id FROM "+schema+".unprocessed_messages WHERE source='PVMBG'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := repo.QueuePVMBGReplay(ctx, epoch, []string{id}); err != nil {
		t.Fatal(err)
	}
	// A failed write schedules backoff without marking the raw message recovered.
	exec("ALTER TABLE " + schema + ".hazard_events ADD CONSTRAINT test_reject_replay CHECK (source_ref_id <> 'bad')")
	if err := m.retryPVMBG(ctx, "replay-down"); err != nil {
		t.Fatal(err)
	}
	var next time.Time
	var attempts int
	if err := conn.QueryRow(ctx, "SELECT status, attempts, next_retry_at FROM "+schema+".unprocessed_messages").Scan(&status, &attempts, &next); err != nil ||
		status != "retrying" || attempts != 2 || !next.After(time.Now()) {
		t.Fatalf("expected backoff: status=%s attempts=%d next=%s error=%v", status, attempts, next, err)
	}
	exec("ALTER TABLE " + schema + ".hazard_events DROP CONSTRAINT test_reject_replay")
	if err := m.retryPVMBG(ctx, "before-due"); err != nil {
		t.Fatal(err)
	}
	assertCount("hazard_events", 1)
	exec("UPDATE " + schema + ".unprocessed_messages SET next_retry_at=CURRENT_TIMESTAMP WHERE source='PVMBG'")
	if err := m.retryPVMBG(ctx, "replay-after-backoff"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := repo.QueuePVMBGReplay(ctx, epoch, []string{id}); err != nil {
			t.Fatal(err)
		}
		if err := m.retryPVMBG(ctx, "repeat"); err != nil {
			t.Fatal(err)
		}
	}
	assertCount("hazard_events", 2)
	assertCount("hazard_outbox", 2)
	if err := conn.QueryRow(ctx, "SELECT status, raw FROM "+schema+".unprocessed_messages").Scan(&status, &raw); err != nil ||
		status != "recovered" || string(raw) != bad {
		t.Fatalf("replay did not preserve the recovered message: status=%s error=%v", status, err)
	}
	if after, err := repo.LoadCursor(ctx, "PVMBG"); err != nil || after != cursor {
		t.Fatalf("replay unexpectedly changed checkpoint: %v", err)
	}
	// A delayed revision must not replace an already newer canonical report.
	superseded := source.DecodePVMBGReplay([]byte(`{"report_id":"bad","volcano_id":"V-001","reported_at":"2026-11-18T00:00:00Z","status":"AWAS"}`))
	event, err := mapPVMBGRecord(superseded)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplayPVMBGEvent(ctx, epoch, event, "outdated-replay"); err != nil {
		t.Fatal(err)
	}
	assertCount("hazard_outbox", 2)
	var severity string
	if err := conn.QueryRow(ctx, "SELECT severity FROM "+schema+".hazard_events WHERE source_ref_id='bad'").Scan(&severity); err != nil || severity != "SIAGA" {
		t.Fatalf("older replay replaced canonical data: severity=%s error=%v", severity, err)
	}
	sendLarge.Store(true)
	m.pollPVMBG(ctx, "oversize-response")
	var size int
	var digest string
	err = conn.QueryRow(ctx, "SELECT octet_length(raw), encode(sha256(raw),'hex') FROM "+schema+".unprocessed_messages WHERE reason='too_large'").Scan(&size, &digest)
	if err != nil || size != len(large) || digest != fmt.Sprintf("%x", sha256.Sum256([]byte(large))) {
		t.Fatalf("oversized body not archived byte for byte: size=%d err=%v", size, err)
	}
	if after, err := repo.LoadCursor(ctx, "PVMBG"); err != nil || after != cursor {
		t.Fatalf("oversized response unexpectedly changed checkpoint: %v", err)
	}
	sendLarge.Store(false)
	sendTransient.Store(true)
	exec("ALTER TABLE " + schema + ".hazard_events ADD CONSTRAINT test_reject_ingest CHECK (source_ref_id <> 'transient')")
	m.pollPVMBG(ctx, "ingest-transient")
	err = conn.QueryRow(ctx, "SELECT status, next_retry_at FROM "+schema+".unprocessed_messages WHERE source_ref_id='transient'").Scan(&status, &next)
	if err != nil || status != "retrying" || !next.After(time.Now()) {
		t.Fatalf("transient live failure was not queued: status=%s next=%s err=%v", status, next, err)
	}
	if after, err := repo.LoadCursor(ctx, "PVMBG"); err != nil || after != cursor {
		t.Fatalf("live write failure unexpectedly changed checkpoint: %v", err)
	}
}

func TestPVMBGReplayKeepsSourceOrder(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL for the PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := pgx.Identifier{fmt.Sprintf("replay_order_%d", time.Now().UnixNano())}.Sanitize()
	exec := func(query string) {
		t.Helper()
		if _, err := conn.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	defer conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	repo, err := store.OpenRouter(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	repo.CheckUnits(ctx)
	if err := repo.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	const epoch = 4
	if err := repo.Fence(ctx, epoch); err != nil {
		t.Fatal(err)
	}
	m := NewManager(nil, nil, repo, time.Second, nil)
	if err := m.PrepareTerm(ctx, epoch, func(context.Context) error { return nil }, repo); err != nil {
		t.Fatal(err)
	}
	first := []byte(`{"report_id":"ordered","volcano_id":"V-001","reported_at":"2026-11-19T00:00:00Z","status":"SIAGA"}`)
	second := []byte(`{"report_id":"ordered","volcano_id":"V-001","reported_at":"2026-11-19T00:00:01Z","status":"AWAS"}`)
	ids := make([]string, 0, 2)
	for i, raw := range [][]byte{first, second} {
		id := fmt.Sprintf("pvmbg:%x", sha256.Sum256(raw))
		ids = append(ids, id)
		msg := deadletter.Message{MessageID: id, Source: "PVMBG", SourceRefID: "ordered", Raw: raw,
			Reason: deadletter.ReasonSchemaIncompatible, Attempts: 1, Status: deadletter.StatusParked,
			ReceivedAt: time.Now().UTC().Add(time.Duration(i) * time.Second)}
		if err := repo.RecordPVMBG(ctx, epoch, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.QueuePVMBGReplay(ctx, epoch, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "UPDATE "+schema+".unprocessed_messages SET next_retry_at=CURRENT_TIMESTAMP+interval '1 hour' WHERE message_id=$1", ids[0]); err != nil {
		t.Fatal(err)
	}
	if pending, err := repo.PendingPVMBGReplay(ctx, time.Now()); err != nil || pending != nil {
		t.Fatalf("newer revision bypassed the delayed head: pending=%v err=%v", pending, err)
	}
	exec("UPDATE " + schema + ".unprocessed_messages SET next_retry_at=CURRENT_TIMESTAMP")
	for i, severity := range []string{"SIAGA", "AWAS"} {
		if err := m.retryPVMBG(ctx, "ordered-replay"); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := conn.QueryRow(ctx, "SELECT severity FROM "+schema+".hazard_events WHERE source_ref_id='ordered'").Scan(&got); err != nil || got != severity {
			t.Fatalf("replay step %d: severity=%s want=%s err=%v", i, got, severity, err)
		}
	}
	if err := repo.QueuePVMBGReplay(ctx, epoch, ids); err != nil {
		t.Fatal(err)
	}
	if err := m.retryPVMBG(ctx, "duplicate-replay"); err != nil {
		t.Fatal(err)
	}
	var hazards, snapshots int
	if err := conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM "+schema+".hazard_events),(SELECT count(*) FROM "+schema+".hazard_outbox)").Scan(&hazards, &snapshots); err != nil || hazards != 1 || snapshots != 2 {
		t.Fatalf("replay dedup: hazards=%d snapshots=%d err=%v", hazards, snapshots, err)
	}
}
