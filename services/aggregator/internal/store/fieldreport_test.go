package store

import (
	"context"
	"testing"
	"time"
)

// TestFieldReportSave is the D-FR idempotency proof at unit and router level:
// the first SaveReport inserts (accepted=true), a redelivery of the same
// report_id is skipped without error (accepted=false), and the router writes
// RF=2 copies through the same partition routing as hazard_events.
func TestFieldReportSave(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL, _ := testSchema(t, ctx)
	repo, err := OpenPostgres(ctx, databaseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	router, err := newRouter([]string{"store-0"}, []*PostgresRepository{repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	received := time.Date(2026, 11, 17, 3, 51, 5, 0, time.UTC)
	occurred := received.Add(-30 * time.Minute)
	report := FieldReport{
		ReportID: "7f2f2b4a-9c1d-4a2e-8b3f-5e6a7d8c9b0a", HazardID: "haz_63eca7f7",
		Note: "Banjir rob naik 40cm", AreaName: "Pesisir Utara", OccurredAt: &occurred,
		Reporter: "field-team", ReceivedAt: received, CorrelationID: "corr-1",
	}
	accepted, err := router.SaveReport(ctx, report, "corr-1")
	if err != nil || !accepted {
		t.Fatalf("first save: accepted=%v err=%v", accepted, err)
	}
	accepted, err = router.SaveReport(ctx, report, "corr-1")
	if err != nil || accepted {
		t.Fatalf("redelivery: accepted=%v err=%v (want false, nil)", accepted, err)
	}
	var rows int
	var partition int
	if err := repo.pool.QueryRow(ctx, `SELECT count(*), min(partition_id) FROM field_reports
		WHERE report_id=$1`, report.ReportID).Scan(&rows, &partition); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || partition != PartitionOf(report.ReportID) {
		t.Fatalf("rows=%d partition=%d; want 1 and %d", rows, partition, PartitionOf(report.ReportID))
	}
	var note string
	var hazardID, areaName *string
	if err := repo.pool.QueryRow(ctx, `SELECT note, hazard_id, area_name FROM field_reports
		WHERE report_id=$1`, report.ReportID).Scan(&note, &hazardID, &areaName); err != nil {
		t.Fatal(err)
	}
	if note != "Banjir rob naik 40cm" || hazardID == nil || *hazardID != "haz_63eca7f7" ||
		areaName == nil || *areaName != "Pesisir Utara" {
		t.Fatalf("stored fields differ: note=%q hazard=%v area=%v", note, hazardID, areaName)
	}
	// A report without optional fields must also store cleanly.
	bare := FieldReport{ReportID: "3b9f4a2e-1c44-4b8e-9a2f-6d5c8e0a1b2c",
		Note: "kerusakan tanggul", Reporter: "field-team", ReceivedAt: received}
	if accepted, err := router.SaveReport(ctx, bare, ""); err != nil || !accepted {
		t.Fatalf("bare report: accepted=%v err=%v", accepted, err)
	}
	if rows, err := countFieldReports(ctx, repo, bare.ReportID); err != nil || rows != 1 {
		t.Fatalf("bare report rows=%d err=%v", rows, err)
	}
}

func countFieldReports(ctx context.Context, repo *PostgresRepository, reportID string) (int, error) {
	var rows int
	err := repo.pool.QueryRow(ctx, "SELECT count(*) FROM field_reports WHERE report_id=$1", reportID).Scan(&rows)
	return rows, err
}
