package reporting

import (
	"context"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

// RouterStore adapts the sharded store Router into the processor's ReportStore
// port. The idempotent insert lives in the router (RF=2 replicas, PRIMARY KEY
// report_id), so a redelivered message maps to accepted=false, and a transient
// store failure maps to an error that keeps the message queued (D-FR).
type RouterStore struct {
	router *store.Router
}

// NewRouterStore returns the durable effect for the field report processor.
func NewRouterStore(router *store.Router) RouterStore { return RouterStore{router: router} }

// SaveReport converts the wire report into its stored form.
func (s RouterStore) SaveReport(ctx context.Context, report Report, correlationID string) (bool, error) {
	return s.router.SaveReport(ctx, store.FieldReport{
		ReportID:      report.ReportID,
		HazardID:      report.HazardID,
		Note:          report.Note,
		AreaName:      report.AreaName,
		OccurredAt:    report.OccurredAt,
		Reporter:      report.Reporter,
		ReceivedAt:    report.ReceivedAt,
		CorrelationID: correlationID,
	}, correlationID)
}
