package coastal

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type checkpointFailure struct {
	Repository
	fail bool
}

func (r *checkpointFailure) SaveCursorReplicated(ctx context.Context, epoch int64, source, raw string) error {
	if r.fail {
		return errors.New("simulated crash before checkpoint")
	}
	return r.Repository.SaveCursorReplicated(ctx, epoch, source, raw)
}

func TestCoastalPostgresReplayAfterCheckpointFailure(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for real PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := pgx.Identifier{fmt.Sprintf("coastal_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", strings.Trim(schema, `"`))
	u.RawQuery = query.Encode()
	repo, err := store.OpenRouter(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	repo.CheckUnits(ctx)
	if err := repo.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Fence(ctx, 3); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{batch: page(normalRecord+",null", `"committed-next"`)}
	fault := &checkpointFailure{Repository: repo, fail: true}
	p := processor(t, source, fault)
	if err := p.Poll(ctx, "crash-before-checkpoint"); err == nil {
		t.Fatal("checkpoint fault hidden")
	}
	if saved, err := repo.LoadCursor(ctx, Source); err != nil || saved != "" {
		t.Fatalf("checkpoint advanced: %s %v", saved, err)
	}
	fault.fail = false
	restarted := processor(t, source, fault)
	if err := restarted.Poll(ctx, "replay-after-restart"); err != nil {
		t.Fatal(err)
	}
	var hazards, snapshots, parked int
	err = conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM "+schema+".hazard_events),(SELECT count(*) FROM "+schema+".hazard_outbox),(SELECT count(*) FROM "+schema+".unprocessed_messages)").Scan(&hazards, &snapshots, &parked)
	if err != nil || hazards != 1 || snapshots != 1 || parked != 1 {
		t.Fatalf("effects duplicated: hazards=%d snapshots=%d parked=%d err=%v", hazards, snapshots, parked, err)
	}
	var raw []byte
	if err := conn.QueryRow(ctx, "SELECT raw FROM "+schema+".unprocessed_messages").Scan(&raw); err != nil || string(raw) != "null" {
		t.Fatal("original poison bytes not retained", err)
	}
	audit, err := repo.Unprocessed(ctx, Source, "", 100)
	if err != nil || len(audit.Items) != 1 || audit.Items[0].Retryable || audit.Items[0].Source != Source {
		t.Fatalf("coastal DLQ metadata: %+v %v", audit, err)
	}
	if processor(t, source, repo).state.Cursor != "committed-next" {
		t.Fatal("committed cursor not restored")
	}
	if err := repo.Fence(ctx, 4); err != nil {
		t.Fatal(err)
	}
	source.batch = page(normalRecord, `"new-position"`)
	if err := restarted.Poll(ctx, "fenced"); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("old term wrote after takeover: %v", err)
	}
}
