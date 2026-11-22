package ingest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

// TestPVMBGReplayParkedCopy replays a copy of real parked PVMBG DLQ rows (JSON
// lines {mid, ref, at, raw} from scripts/m2/p34-replay-copy.sh) through the
// production replay path on a disposable database. It never touches a cluster.
func TestPVMBGReplayParkedCopy(t *testing.T) {
	dbURL, fixture := os.Getenv("TEST_DATABASE_URL"), os.Getenv("P34_PARKED_FIXTURE")
	if dbURL == "" || fixture == "" {
		t.Skip("set TEST_DATABASE_URL and P34_PARKED_FIXTURE to replay a copy of parked DLQ rows")
	}
	type row struct {
		MID string    `json:"mid"`
		Ref *string   `json:"ref"`
		At  time.Time `json:"at"`
		Raw string    `json:"raw"`
	}
	file, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows := map[string]row{} // the fixture holds one line per replica copy
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 2<<20), 2<<20)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var r row
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		rows[r.MID] = r
	}
	if err := scanner.Err(); err != nil || len(rows) == 0 {
		t.Fatalf("fixture: rows=%d err=%v", len(rows), err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := pgx.Identifier{fmt.Sprintf("p34_copy_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	u, _ := url.Parse(dbURL)
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	repo, err := store.OpenRouterWithMemoryMap(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	repo.CheckUnits(ctx)
	if err := repo.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	const epoch = 7
	if err := repo.Fence(ctx, epoch); err != nil {
		t.Fatal(err)
	}
	m := NewManager(nil, nil, repo, time.Second, nil)
	if err := m.PrepareTerm(ctx, epoch, func(context.Context) error { return nil }, repo); err != nil {
		t.Fatal(err)
	}

	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		msg := deadletter.Message{MessageID: r.MID, Source: "PVMBG", Raw: []byte(r.Raw),
			Reason: deadletter.ReasonSchemaIncompatible, Attempts: 1, Status: deadletter.StatusParked, ReceivedAt: r.At}
		if r.Ref != nil {
			msg.SourceRefID = *r.Ref
		}
		if err := repo.RecordPVMBG(ctx, epoch, msg); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.MID)
	}
	replayAll := func(label string) {
		t.Helper()
		for start := 0; start < len(ids); start += 32 { // the HTTP endpoint accepts 32 IDs per call
			if err := repo.QueuePVMBGReplay(ctx, epoch, ids[start:min(start+32, len(ids))]); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 4*len(ids); i++ {
			if err := m.retryPVMBG(ctx, label); err != nil {
				t.Fatal(err)
			}
		}
	}
	type state struct{ hazards, outbox, recovered, other, maxRevision int }
	snapshot := func() (s state) {
		t.Helper()
		err := conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM "+schema+".hazard_events WHERE source='PVMBG'),"+
			"(SELECT count(*) FROM "+schema+".hazard_outbox),"+
			"(SELECT count(*) FROM "+schema+".unprocessed_messages WHERE status='recovered'),"+
			"(SELECT count(*) FROM "+schema+".unprocessed_messages WHERE status<>'recovered'),"+
			"(SELECT COALESCE(max(revision),0) FROM "+schema+".hazard_events)").Scan(&s.hazards, &s.outbox, &s.recovered, &s.other, &s.maxRevision)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	replayAll("p34-copy-replay")
	first := snapshot()
	t.Logf("after replay: %+v (fixture messages=%d)", first, len(rows))
	if first.recovered != len(rows) || first.other != 0 {
		var reasons []string
		r, _ := conn.Query(ctx, "SELECT message_id||' '||status||' '||reason FROM "+schema+".unprocessed_messages WHERE status<>'recovered'")
		for r.Next() {
			var s string
			r.Scan(&s)
			reasons = append(reasons, s)
		}
		t.Fatalf("not all messages recovered: %+v %v", first, reasons)
	}
	// One canonical event per report, keyed by the report's own ID, with the
	// timestamp and level the raw payload carried.
	for _, r := range rows {
		var report struct {
			ID     string          `json:"report_id"`
			Level  string          `json:"alert_level"`
			Status string          `json:"status"`
			At     json.RawMessage `json:"reported_at"`
		}
		if err := json.Unmarshal([]byte(r.Raw), &report); err != nil {
			t.Fatal(err)
		}
		var count int
		var severity string
		var occurred time.Time
		if err := conn.QueryRow(ctx, "SELECT count(*), max(severity), max(occurred_at) FROM "+schema+".hazard_events WHERE source='PVMBG' AND source_ref_id=$1", report.ID).Scan(&count, &severity, &occurred); err != nil || count != 1 {
			t.Fatalf("%s: canonical events=%d err=%v", report.ID, count, err)
		}
		if level := report.Level + report.Status; severity != level {
			t.Fatalf("%s: severity %s, raw level %s", report.ID, severity, level)
		}
		var seconds int64
		if json.Unmarshal(report.At, &seconds) == nil && !occurred.Equal(time.Unix(seconds, 0)) {
			t.Fatalf("%s: occurred_at %s, raw epoch %d", report.ID, occurred, seconds)
		}
	}
	if first.hazards != len(rows) {
		t.Fatalf("hazards=%d want %d (one per report)", first.hazards, len(rows))
	}

	replayAll("p34-copy-replay-again")
	if again := snapshot(); again != first {
		t.Fatalf("repeated replay changed storage: before=%+v after=%+v", first, again)
	}
}
