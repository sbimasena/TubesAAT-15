package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

// P5.1: a critical coastal alert queued behind a backlog must be read first; the rest of
// the backlog keeps its FIFO order.
func TestPendingOutboxPutsCriticalCoastalFirst(t *testing.T) {
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
	schema := fmt.Sprintf("outbox_priority_test_%d", time.Now().UnixNano())
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

	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	coastal := func(id string, critical bool) domain.HazardEvent {
		severity := "NORMAL"
		if critical {
			severity = "AWAS"
		}
		return domain.HazardEvent{HazardID: "haz_" + id, Source: "SENSOR_PANTAI", SourceRefID: id, HazardType: "COASTAL",
			Severity: severity, AreaName: "Aceh", Latitude: 5, Longitude: 95, OccurredAt: now, IngestedAt: now,
			Attributes: map[string]any{"critical": critical}}
	}
	seismic := domain.MapSeismic(domain.SeismicEvent{EventID: "seismic-1", RegionName: "Test Area",
		Magnitude: 5.5, EpicenterLat: -7, EpicenterLon: 110, OccurredAt: now}, nil, now)
	// Insert order = FIFO order: two ordinary events, then the critical alert.
	for _, event := range []domain.HazardEvent{seismic, coastal("calm", false), coastal("wave", true)} {
		if _, err := repo.Upsert(ctx, 0, []domain.HazardEvent{event}, "test-priority"); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := repo.PendingOutbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, message := range pending {
		order = append(order, fmt.Sprintf("%s:%t", message.HazardID, message.Critical))
	}
	want := []string{"haz_wave:true", seismic.HazardID + ":false", "haz_calm:false"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("pending order %v, want %v", order, want)
	}
}
