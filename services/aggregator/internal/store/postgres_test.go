package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

func TestPostgresRepository(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the real PostgreSQL integration check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	schema := fmt.Sprintf("storage_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := connection.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer connection.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	endpoint, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := endpoint.Query()
	query.Set("search_path", schema)
	endpoint.RawQuery = query.Encode()
	repo, err := OpenPostgres(ctx, endpoint.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	occurred := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	seismic := domain.MapSeismic(domain.SeismicEvent{EventID: "seismic-1", RegionName: "Test Area",
		Magnitude: 5.5, EpicenterLat: -7, EpicenterLon: 110, OccurredAt: occurred}, nil, occurred)
	v1, err := domain.MapVolcanic(domain.VolcanicReport{ReportID: "volcanic-1", VolcanoID: "V-001",
		AlertLevel: "WASPADA", ReportedAt: occurred}, occurred)
	if err != nil {
		t.Fatal(err)
	}
	upsert := func(events []domain.HazardEvent, correlationID string, want int) {
		t.Helper()
		changed, err := repo.Upsert(ctx, events, correlationID)
		if err != nil || len(changed) != want {
			t.Fatalf("Upsert: changed=%d want=%d err=%v", len(changed), want, err)
		}
	}
	counts := func(wantEvents, wantOutbox int) {
		t.Helper()
		var events, outbox int
		if err := repo.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM hazard_events),
			(SELECT count(*) FROM hazard_outbox WHERE published_at IS NULL)`).Scan(&events, &outbox); err != nil {
			t.Fatal(err)
		}
		if events != wantEvents || outbox != wantOutbox {
			t.Fatalf("counts: events=%d outbox=%d; want %d/%d", events, outbox, wantEvents, wantOutbox)
		}
	}
	upsert([]domain.HazardEvent{seismic, v1}, "test-initial", 2)
	seismic.IngestedAt = occurred.Add(time.Hour)
	upsert([]domain.HazardEvent{seismic, v1}, "test-repoll", 0)
	counts(2, 2)

	updated := domain.MapSeismic(domain.SeismicEvent{EventID: "seismic-1", RegionName: "Test Area",
		Magnitude: 5.5, EpicenterLat: -7, EpicenterLon: 110, OccurredAt: occurred},
		&domain.TsunamiWarning{WarningID: "warning-1", ThreatLevel: "AWAS", IssuedAt: occurred}, occurred.Add(time.Hour))
	upsert([]domain.HazardEvent{updated}, "test-warning", 1)
	var revision int
	var originalSeverity, updatedSeverity, correlationID string
	if err := repo.pool.QueryRow(ctx, `SELECT hazard_revision, payload->>'severity', correlation_id
		FROM hazard_outbox WHERE hazard_id=$1 ORDER BY hazard_revision DESC LIMIT 1`, seismic.HazardID).
		Scan(&revision, &updatedSeverity, &correlationID); err != nil {
		t.Fatal(err)
	}
	if err := repo.pool.QueryRow(ctx, `SELECT payload->>'severity' FROM hazard_outbox
		WHERE hazard_id=$1 AND hazard_revision=1`, seismic.HazardID).Scan(&originalSeverity); err != nil {
		t.Fatal(err)
	}
	if revision != 2 || updatedSeverity != "AWAS" || originalSeverity != "WASPADA" || correlationID != "test-warning" {
		t.Fatal("update revision, correlation ID, or immutable outbox snapshot is incorrect")
	}
	confidence := 0.9
	v2, err := domain.MapVolcanic(domain.VolcanicReport{ReportID: "volcanic-2", VolcanoID: "V-001",
		AlertLevel: "SIAGA", ReportedAt: occurred.Add(time.Minute), ConfidenceLevel: &confidence}, occurred)
	if err != nil {
		t.Fatal(err)
	}
	upsert([]domain.HazardEvent{v2}, "test-schema-v2", 1)
	rows, err := repo.List(ctx, Filter{Source: "PVMBG", HazardType: "VOLCANIC", Limit: 10}, "test-read")
	if err != nil || len(rows) != 2 {
		t.Fatalf("read v1+v2 together: rows=%d err=%v", len(rows), err)
	}
	if rows[0].Attributes["confidence_level"] != confidence {
		t.Fatal("new field was not retained in JSONB")
	}
	if _, exists := rows[1].Attributes["confidence_level"]; exists {
		t.Fatal("old record must not invent a confidence_level")
	}
	filtered, err := repo.List(ctx, Filter{Since: occurred.Add(time.Minute), Limit: 1}, "test-filter")
	if err != nil || len(filtered) != 1 || filtered[0].HazardID != v2.HazardID {
		t.Fatal("since, ordering, or limit filter is incorrect", err)
	}
	counts(3, 4)

	newEvent := seismic
	newEvent.HazardID, newEvent.SourceRefID = "rollback-hazard", "rollback-source"
	invalid := newEvent
	invalid.HazardID, invalid.SourceRefID, invalid.Severity = "invalid-hazard", "invalid-source", "INVALID"
	if _, err := repo.Upsert(ctx, []domain.HazardEvent{newEvent, invalid}, "test-rollback"); err == nil {
		t.Fatal("invalid batch must fail")
	}
	counts(3, 4)
	if _, err := repo.pool.Exec(ctx, `ALTER TABLE hazard_outbox ADD CONSTRAINT test_failure
		CHECK (correlation_id <> 'force-outbox-failure')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Upsert(ctx, []domain.HazardEvent{newEvent}, "force-outbox-failure"); err == nil {
		t.Fatal("outbox failure must roll back the canonical write")
	}
	counts(3, 4)

	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := repo.Upsert(ctx, []domain.HazardEvent{newEvent}, "test-concurrent"); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	counts(4, 5)
	var migratedAt time.Time
	if err := repo.pool.QueryRow(ctx, "SELECT applied_at FROM schema_migrations WHERE version=1").Scan(&migratedAt); err != nil {
		t.Fatal(err)
	}
	repo.Close()
	repo, err = OpenPostgres(ctx, endpoint.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	counts(4, 5)
	var afterReopen time.Time
	if err := repo.pool.QueryRow(ctx, "SELECT applied_at FROM schema_migrations WHERE version=1").Scan(&afterReopen); err != nil {
		t.Fatal(err)
	}
	if !afterReopen.Equal(migratedAt) {
		t.Fatal("reopening the repository must not reapply the migration")
	}
	t.Log("PASS: idempotent polling, update snapshots/revisions, v1+v2 JSONB, filters, atomic rollback, concurrent deduplication, and reopen without migration")
}
