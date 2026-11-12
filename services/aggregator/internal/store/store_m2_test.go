package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

func TestPartitionOfIsStable(t *testing.T) {
	// Reference values from an independent fnv32a implementation; changing them reshuffles every unit.
	for key, want := range map[string]int{"": 13, "haz_0": 15, "hz-7f3a91": 1, "msg-31c9": 13} {
		if got := PartitionOf(key); got != want {
			t.Fatalf("PartitionOf(%q)=%d want %d", key, got, want)
		}
	}
}

func TestChecksumMatchesContract(t *testing.T) {
	// kontrak-m2.md §7.3: byte order (uppercase first), "\n" separator, no trailing newline.
	result := checksumOf("SATELIT", []string{"b-1", "A-1", "B-2"}, time.Unix(0, 0))
	if result.Count != 3 || result.Checksum != "6c566a7b54aaba10c93f3846a411eb3fff8268169c1810740f7fe58216d26845" {
		t.Fatalf("checksum=%s count=%d", result.Checksum, result.Count)
	}
	if result.UnavailableUnits == nil {
		t.Fatal("unavailable units must encode as [] not null")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	want := pageCursor{IngestedAt: time.Date(2026, 11, 12, 2, 51, 8, 123456000, time.UTC), HazardID: "haz_1"}
	got, err := decodeCursor(encodeCursor(want))
	if err != nil || !got.IngestedAt.Equal(want.IngestedAt) || got.HazardID != want.HazardID {
		t.Fatalf("got %+v err=%v", got, err)
	}
	for _, bad := range []string{"%%%", "e30", "bm90LWpzb24"} {
		if _, err := decodeCursor(bad); err == nil {
			t.Fatalf("cursor %q must be rejected", bad)
		}
	}
}

// testSchema creates an isolated schema and returns a DATABASE_URL pointing at it.
func testSchema(t *testing.T, ctx context.Context) (string, *pgx.Conn) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the real PostgreSQL integration check")
	}
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close(context.Background()) })
	schema := fmt.Sprintf("storage_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := connection.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	endpoint, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := endpoint.Query()
	query.Set("search_path", schema)
	endpoint.RawQuery = query.Encode()
	if _, err := connection.Exec(ctx, "SET search_path TO "+quoted); err != nil {
		t.Fatal(err)
	}
	return endpoint.String(), connection
}

func m2Event(source, hazardType, ref string, ingested time.Time) domain.HazardEvent {
	return domain.HazardEvent{HazardID: "haz_" + ref, Source: source, SourceRefID: ref,
		HazardType: hazardType, Severity: "SIAGA", AreaName: "Kalimantan Tengah",
		Latitude: -2.5, Longitude: 113.8, OccurredAt: ingested.Add(-time.Minute),
		IngestedAt: ingested, Attributes: map[string]any{"detection_count": 6}}
}

func TestStoreM2(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL, _ := testSchema(t, ctx)
	repo, err := OpenPostgres(ctx, databaseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	repo.SetIdentity("store-1", "node-b")

	base := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	fire := m2Event("SATELIT", "WILDFIRE", "fire-1", base)
	coastal := m2Event("SENSOR_PANTAI", "COASTAL", "coast-1", base.Add(time.Second))
	fire2 := m2Event("SATELIT", "WILDFIRE", "fire-0", base.Add(2*time.Second))
	if _, err := repo.Upsert(ctx, 0, []domain.HazardEvent{fire, coastal, fire2}, "m2-insert"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Upsert(ctx, 0, []domain.HazardEvent{m2Event("SATELIT", "SEISMIC", "bad", base)}, "m2-mismatch"); err == nil {
		t.Fatal("source/hazard_type pairing must be enforced")
	}

	var partition int
	var leaked bool
	if err := repo.pool.QueryRow(ctx, `SELECT e.partition_id, o.payload ? 'partition_id'
		FROM hazard_events e JOIN hazard_outbox o USING (hazard_id) WHERE hazard_id=$1`,
		fire.HazardID).Scan(&partition, &leaked); err != nil {
		t.Fatal(err)
	}
	if partition != PartitionOf(fire.HazardID) || leaked {
		t.Fatalf("partition_id=%d leaked into outbox=%v", partition, leaked)
	}

	first, err := repo.Query(ctx, Filter{Limit: 2}, "")
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil || first.Items[0].HazardID != fire.HazardID {
		t.Fatalf("first page: %+v err=%v", first, err)
	}
	second, err := repo.Query(ctx, Filter{Limit: 2}, *first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].HazardID != fire2.HazardID {
		t.Fatalf("second page: %+v err=%v", second, err)
	}
	since, err := repo.Query(ctx, Filter{Source: "SATELIT", IngestedSince: base.Add(time.Second)}, "")
	if err != nil || len(since.Items) != 1 || since.Items[0].HazardID != fire2.HazardID {
		t.Fatalf("source+since filter: %+v err=%v", since, err)
	}
	if _, err := repo.Query(ctx, Filter{}, "not-a-cursor"); err == nil {
		t.Fatal("invalid cursor must be rejected")
	}

	sum, err := repo.Checksum(ctx, "SATELIT")
	if err != nil || sum.Count != 2 || sum.Checksum != checksumOf("", []string{"fire-0", "fire-1"}, time.Time{}).Checksum {
		t.Fatalf("checksum: %+v err=%v", sum, err)
	}
	if all, err := repo.Checksum(ctx, ""); err != nil || all.Count != 3 {
		t.Fatalf("checksum over all sources: %+v err=%v", all, err)
	}

	locations, _, err := repo.Locate(ctx, fire.HazardID)
	if err != nil || len(locations) != 1 || locations[0] != (Location{Node: "node-b", Unit: "store-1", Role: "primary"}) {
		t.Fatalf("locate: %+v err=%v", locations, err)
	}
	if missing, _, err := repo.Locate(ctx, "haz_missing"); err != nil || len(missing) != 0 {
		t.Fatalf("unknown hazard: %+v err=%v", missing, err)
	}

	if cursor, err := repo.LoadCursor(ctx, "BMKG"); err != nil || cursor != "" {
		t.Fatalf("empty cursor: %q err=%v", cursor, err)
	}
	if err := repo.SaveCursor(ctx, 0, "BMKG", "2026-11-12T02:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Fence(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveCursor(ctx, 4, "BMKG", "zombie"); !errors.Is(err, ErrFenced) {
		t.Fatalf("stale SaveCursor: %v", err)
	}
	zombie := m2Event("SATELIT", "WILDFIRE", "fire-zombie", base)
	if _, err := repo.Upsert(ctx, 4, []domain.HazardEvent{zombie}, "m2-zombie"); !errors.Is(err, ErrFenced) {
		t.Fatalf("stale Upsert: %v", err)
	}
	if err := repo.Fence(ctx, 3); !errors.Is(err, ErrFenced) {
		t.Fatalf("lower fence must be rejected: %v", err)
	}
	if err := repo.Fence(ctx, 5); err != nil {
		t.Fatalf("repeating the current fence must succeed: %v", err)
	}
	if cursor, err := repo.LoadCursor(ctx, "BMKG"); err != nil || cursor != "2026-11-12T02:00:00Z" {
		t.Fatalf("fenced write must not move the cursor: %q err=%v", cursor, err)
	}
	if _, err := repo.Upsert(ctx, 6, []domain.HazardEvent{zombie}, "m2-leader"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Fence(ctx, 5); !errors.Is(err, ErrFenced) {
		t.Fatalf("a write with epoch 6 must raise the fence: %v", err)
	}
}

func TestMigrationBackfillsM1Rows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL, connection := testSchema(t, ctx)
	v1, err := migrationFiles.ReadFile("migrations/001_canonical.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, string(v1)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO schema_migrations (version) VALUES (1);
		INSERT INTO hazard_events (hazard_id, source, source_ref_id, hazard_type, severity, area_name,
		latitude, longitude, occurred_at, ingested_at, attributes) VALUES ('haz_m1', 'BMKG', 'm1',
		'SEISMIC', 'NORMAL', 'Area', 0, 0, now(), now(), '{}')`); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenPostgres(ctx, databaseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	var partition int
	if err := repo.pool.QueryRow(ctx, "SELECT partition_id FROM hazard_events WHERE hazard_id='haz_m1'").
		Scan(&partition); err != nil || partition != PartitionOf("haz_m1") {
		t.Fatalf("backfill: partition=%d err=%v", partition, err)
	}
	var version int64
	if err := repo.pool.QueryRow(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
}
