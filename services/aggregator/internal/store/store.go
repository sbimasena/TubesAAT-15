package store

import (
	"context"
	"errors"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

// ErrFenced is returned when a write carries an epoch older than the one a
// storage unit has already accepted (D-FENCE).
var ErrFenced = errors.New("store: stale epoch")

// ErrInvalidCursor is returned by Query for a cursor it did not issue.
var ErrInvalidCursor = errors.New("store: invalid cursor")

type Filter struct {
	Source     string
	HazardType string
	Since      time.Time // List: occurred_at >= Since.
	// IngestedSince is the Audit API `since` (ingested_at >= IngestedSince), used by Query.
	IngestedSince time.Time
	Limit         int
}

type Page struct {
	Items            []domain.HazardEvent
	NextCursor       *string  // nil on the last page.
	Partial          bool     // true only when some partition has no live replica.
	UnavailableUnits []string // set whenever a unit could not be reached.
}

type ChecksumResult struct {
	Source           string
	Count            int
	Checksum         string // lowercase hex SHA-256 over byte-sorted source_ref_id joined by "\n".
	AsOf             time.Time
	Partial          bool
	UnavailableUnits []string
}

// Analytics feeds the P1.7 dashboard; Total over all time equals the Audit API checksum count.
type Analytics struct {
	From             time.Time        `json:"from"`
	To               time.Time        `json:"to"`
	Buckets          []time.Time      `json:"buckets"` // start of each equal-width bucket
	Series           map[string][]int `json:"series"`  // hazard_type -> count per bucket
	BySeverity       map[string]int   `json:"by_severity"`
	ByArea           map[string]int   `json:"by_area"`
	Total            int              `json:"total"`
	Partial          bool             `json:"partial"`
	UnavailableUnits []string         `json:"unavailable_units"`
}

type Location struct {
	Node string // Kubernetes node name.
	Unit string
	Role string // "primary" or "replica".
}

// Store is the Aggregator-facing storage API agreed for M2 (pembagian-kerja-m2.md §5).
// Every method honours ctx and a per-unit timeout; none hangs on a dead unit.
type Store interface {
	// Fence is the leader barrier, called before any leader-only worker starts.
	Fence(ctx context.Context, epoch int64) error
	// Upsert returns ErrFenced when epoch is stale.
	Upsert(ctx context.Context, epoch int64, events []domain.HazardEvent, correlationID string) ([]domain.HazardEvent, error)
	Query(ctx context.Context, f Filter, cursor string) (Page, error)
	Checksum(ctx context.Context, source string) (ChecksumResult, error)
	// Locate returns no locations and a nil error when hazardID is unknown.
	Locate(ctx context.Context, hazardID string) (locations []Location, partial bool, err error)
	// SaveCursor is called only after the matching Upsert committed.
	SaveCursor(ctx context.Context, epoch int64, source, cursor string) error
	// LoadCursor returns "" when no cursor has been saved for source.
	LoadCursor(ctx context.Context, source string) (string, error)
}

// Repository is the narrower M1 surface still used by ingest and the HTTP API.
type Repository interface {
	Upsert(ctx context.Context, epoch int64, events []domain.HazardEvent, correlationID string) ([]domain.HazardEvent, error)
	List(context.Context, Filter, string) ([]domain.HazardEvent, error)
	Ping(context.Context) error
}
