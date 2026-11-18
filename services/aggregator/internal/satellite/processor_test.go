package satellite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type crashRepository struct {
	Repository
	fail                  bool
	saveCalls, failSaveAt int
}

func (r *crashRepository) UpsertReplicated(ctx context.Context, epoch int64, events []domain.HazardEvent, id string) ([]domain.HazardEvent, error) {
	if r.fail {
		return nil, errors.New("simulated crash before materialization")
	}
	return r.Repository.UpsertReplicated(ctx, epoch, events, id)
}

// All storage units use independent schemas of a disposable PostgreSQL. This
// exercises durable recovery and Audit views, without claiming VM/node failover.
func postgresRouter(t *testing.T) *store.Router {
	t.Helper()
	endpoint := os.Getenv("TEST_DATABASE_URL")
	if endpoint == "" {
		t.Skip("set TEST_DATABASE_URL for durable satellite recovery")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) })
	urls := ""
	for i := 0; i < 3; i++ {
		schema := fmt.Sprintf("satellite_test_%d_%d", time.Now().UnixNano(), i)
		quoted := pgx.Identifier{schema}.Sanitize()
		if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE") })
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		q.Set("host", u.Hostname())
		q.Set("port", u.Port())
		u.RawQuery = q.Encode()
		u.Host = fmt.Sprintf("unit%d", i)
		if i > 0 {
			urls += ","
		}
		urls += u.String()
	}
	router, err := store.OpenRouter(urls, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Close)
	router.CheckUnits(ctx)
	if err := router.LoadMap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := router.Fence(ctx, 1); err != nil {
		t.Fatal(err)
	}
	return router
}

func TestProcessorRecoversPendingEffectsFromPostgres(t *testing.T) {
	router := postgresRouter(t)
	ctx := context.Background()
	verify := func(context.Context) error { return nil }
	crashing := &crashRepository{Repository: router, fail: true}
	processor, err := NewProcessor(crashing, 1, verify)
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	a, b := detection("N20", "0", "113.80000"), detection("N21", "1", "113.80100")
	if err := processor.Process(ctx, []Mutation{mutate(1, a), mutate(2, b)}, now); err == nil {
		t.Fatal("simulated crash did not stop commit")
	}
	if _, err := processor.Offset(); err == nil {
		t.Fatal("pending effects allowed a new stream")
	}
	if sum, err := router.Checksum(ctx, "SATELIT"); err != nil || sum.Count != 0 {
		t.Fatalf("premature effects: %+v %v", sum, err)
	}
	raw, err := router.LoadCursor(ctx, "SATELIT")
	if err != nil {
		t.Fatal(err)
	}
	var saved recoveryCheckpoint
	if err := json.Unmarshal([]byte(raw), &saved); err != nil || saved.State.Offset != 2 || len(saved.Pending.Upserts) != 1 {
		t.Fatal("recovery plan not durable", err)
	}
	// A new process has no memory from the failed handler and owns a new term.
	if err := router.Fence(ctx, 2); err != nil {
		t.Fatal(err)
	}
	crashing.fail = false
	if err := processor.Process(ctx, nil, now); !errors.Is(err, store.ErrFenced) {
		t.Fatal("old term wrote after takeover", err)
	}
	restored, err := NewProcessor(router, 2, verify)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	sum, err := router.Checksum(ctx, "SATELIT")
	if err != nil || sum.Count != 1 {
		t.Fatalf("recovery: %+v %v", sum, err)
	}
	if err := restored.Process(ctx, []Mutation{mutate(1, a), mutate(2, b)}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	replay, err := router.Checksum(ctx, "SATELIT")
	if err != nil || replay.Checksum != sum.Checksum || replay.Count != sum.Count {
		t.Fatal("replay duplicated effects", err)
	}
	if err := restored.Process(ctx, []Mutation{{Offset: 3, Kind: "retract", Key: a.Key()}}, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	withdrawn, err := router.Checksum(ctx, "SATELIT")
	if err != nil || withdrawn.Count != 0 {
		t.Fatalf("retracted fire remains in checksum: %+v %v", withdrawn, err)
	}
	page, err := router.Query(ctx, store.Filter{Source: "SATELIT"}, "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal("retraction leaked into Audit events", err)
	}
	rows, err := router.List(ctx, store.Filter{Source: "SATELIT"}, "test")
	if err != nil || len(rows) != 0 {
		t.Fatal("retraction leaked into reads", err)
	}
	for _, event := range saved.State.Events {
		locations, _, err := router.Locate(ctx, event.HazardID)
		if err != nil || len(locations) != 0 {
			t.Fatal("retracted fire has active locations", err)
		}
	}
	analytics, err := router.Analytics(ctx, now.Add(-24*time.Hour), now.Add(24*time.Hour), 10)
	if err != nil || analytics.Total != 0 {
		t.Fatal("withdrawal leaked into analytics", err)
	}
	// Reintroducing the surviving pair restores its original identity and checksum.
	if err := restored.Process(ctx, []Mutation{mutate(4, a)}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	returned, err := router.Checksum(ctx, "SATELIT")
	if err != nil || returned.Checksum != sum.Checksum {
		t.Fatal("returned fire changed ID", err)
	}
}

func (r *crashRepository) SaveCursorReplicated(ctx context.Context, epoch int64, source, value string) error {
	r.saveCalls++
	if r.failSaveAt > 0 && r.saveCalls == r.failSaveAt {
		return errors.New("simulated crash after canonical commit")
	}
	return r.Repository.SaveCursorReplicated(ctx, epoch, source, value)
}

func TestCheckpointClearFailureDoesNotDuplicateOutbox(t *testing.T) {
	router := postgresRouter(t)
	ctx := context.Background()
	verify := func(context.Context) error { return nil }
	failure := &crashRepository{Repository: router, failSaveAt: 3}
	p, err := NewProcessor(failure, 1, verify)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	a, b := detection("N20", "0", "113.80000"), detection("N21", "1", "113.80100")
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	if err := p.Process(ctx, []Mutation{mutate(1, a), mutate(2, b)}, now); err == nil {
		t.Fatal("expected checkpoint-clear interruption")
	}
	before, err := router.PendingOutbox(ctx)
	if err != nil || len(before) != 2 {
		t.Fatalf("outbox=%d err=%v", len(before), err)
	}
	restored, err := NewProcessor(router, 1, verify)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := router.PendingOutbox(ctx)
	if err != nil || len(after) != len(before) {
		t.Fatal("recovery duplicated outbox effects", err)
	}
	if offset, err := restored.Offset(); err != nil || offset != 2 {
		t.Fatalf("offset=%d err=%v", offset, err)
	}
}
