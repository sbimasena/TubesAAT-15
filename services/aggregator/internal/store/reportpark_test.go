package store

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestParkReportPreservesBytesAndDeduplicates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url, _ := testSchema(t, ctx)
	repo, err := OpenPostgres(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	router, err := newRouter([]string{"store-0"}, []*PostgresRepository{repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte{'{', 0xff, 0, '}'}
	for range 2 {
		if err := router.ParkReport(ctx, raw, "contract_violation", 6); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := repo.pool.QueryRow(ctx, "SELECT count(*) FROM unprocessed_messages").Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	var saved []byte
	var source, status string
	if err := repo.pool.QueryRow(ctx, "SELECT raw, source, status FROM unprocessed_messages").Scan(&saved, &source, &status); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, raw) || source != "FIELD_REPORT" || status != "parked" {
		t.Fatal("parked payload or metadata changed")
	}
}
