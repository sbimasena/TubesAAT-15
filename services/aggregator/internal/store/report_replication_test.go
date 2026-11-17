package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"
)

// Independent schemas model three storage units without needing a K8s cluster.
// This tests placement and partial-write recovery, not physical node failover.
func TestReportReplicationRequiresAllTargets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	urls := make([]string, 3)
	repos := make([]*PostgresRepository, 3)
	for i := range repos {
		urls[i], _ = testSchema(t, ctx)
		var err error
		repos[i], err = OpenPostgres(ctx, urls[i], nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func(i int) { repos[i].Close() }(i)
	}
	r, err := newRouter([]string{"s0", "s1", "s2"}, repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := FieldReport{ReportID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Note: "RF2 integration", Reporter: "field-team", ReceivedAt: time.Now()}
	targets := r.current().writes[PartitionOf(report.ReportID)]
	if len(targets) != 2 {
		t.Fatalf("expected RF2, got %d", len(targets))
	}
	// A failed replica must prevent success even if another copy commits.
	broken := targets[0]
	old := broken.repo
	old.Close()
	if _, err := r.SaveReport(ctx, report, "rf2-test"); err == nil {
		t.Fatal("partial write falsely reported durable success")
	}
	var index int
	for i, repo := range repos {
		if repo == old {
			index = i
		}
	}
	repos[index], err = OpenPostgres(ctx, urls[index], nil)
	if err != nil {
		t.Fatal(err)
	}
	broken.repo = repos[index]
	if _, err := r.SaveReport(ctx, report, "rf2-test"); err != nil {
		t.Fatal(err)
	}
	copies := 0
	for _, repo := range repos {
		n, err := countFieldReports(ctx, repo, report.ReportID)
		if err != nil {
			t.Fatal(err)
		}
		copies += n
	}
	if copies != 2 {
		t.Fatalf("expected exactly two copies, got %d", copies)
	}
	if accepted, err := r.SaveReport(ctx, report, "rf2-test"); err != nil || accepted {
		t.Fatalf("duplicate accepted=%v err=%v", accepted, err)
	}
	raw := []byte("{malformed-rf2")
	id := fmt.Sprintf("field-report:%x", sha256.Sum256(raw))
	broken = r.current().writes[PartitionOf(id)][0]
	old = broken.repo
	for i, repo := range repos {
		if repo == old {
			index = i
		}
	}
	old.Close()
	if err := r.ParkReport(ctx, raw, "contract_violation", 6); err == nil {
		t.Fatal("partial DLQ write falsely reported durable success")
	}
	repos[index], err = OpenPostgres(ctx, urls[index], nil)
	if err != nil {
		t.Fatal(err)
	}
	broken.repo = repos[index]
	for range 2 {
		if err := r.ParkReport(ctx, raw, "contract_violation", 6); err != nil {
			t.Fatal(err)
		}
	}
	copies = 0
	for _, repo := range repos {
		var n int
		if err := repo.pool.QueryRow(ctx, "SELECT count(*) FROM unprocessed_messages WHERE message_id=$1", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		copies += n
	}
	if copies != 2 {
		t.Fatalf("expected exactly two DLQ copies, got %d", copies)
	}
}
