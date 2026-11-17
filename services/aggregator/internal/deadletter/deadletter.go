// Package deadletter defines the contract for handling messages that cannot be
// processed by the Aggregator (P3): the classification rules, the message
// shape, and the storage interface.
//
// Status: CONTRACT ONLY (decision 16 Nov 2026). The durable implementation
// waits for migration 004 (table unprocessed_messages, D-DLQ: B + C) so the
// router can partition it with the same RF=2 guarantee as hazard_events.
// Nothing in this package may persist to memory-only storage that pretends to
// be durable; see context-m2.md rule 10.
package deadletter

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Status of a stored message.
type Status string

const (
	StatusRetrying  Status = "retrying"  // transient failure, backoff in progress
	StatusParked    Status = "parked"    // permanent failure, needs manual/mapper action
	StatusRecovered Status = "recovered" // reprocessed successfully, no longer listed
)

// Canonical reasons. Retryability is decided from the reason, not ad hoc.
const (
	ReasonTimeout             = "timeout"
	ReasonUpstream5xx         = "upstream_5xx"
	ReasonStoreUnavailable    = "store_unavailable"
	ReasonSchemaIncompatible  = "schema_incompatible"
	ReasonMalformedJSON       = "malformed_json"
	ReasonInvalidUTF8         = "invalid_utf8"
	ReasonTooDeep             = "too_deep"
	ReasonTooLarge            = "too_large"
	ReasonContractViolation   = "contract_violation"
	ReasonInternalPanic       = "internal_panic"
)

// retryableReasons are transient failures: the same message may succeed later,
// so it is retried with backoff and parked only after the attempt budget is
// exhausted.
var retryableReasons = map[string]bool{
	ReasonTimeout:          true,
	ReasonUpstream5xx:      true,
	ReasonStoreUnavailable: true,
	ReasonInternalPanic:    true,
}

// Retryable reports whether a reason is transient.
func Retryable(reason string) bool {
	return retryableReasons[reason]
}

// Message is one parked or retrying payload, stored verbatim (P3.2: "pesan
// gagal disimpan utuh + alasan + sumber + waktu").
type Message struct {
	MessageID    string
	Source       string // BMKG | PVMBG | SENSOR_PANTAI | SATELIT
	Raw          []byte // untouched wire bytes, never re-serialized
	Reason       string
	SourceRefID  string // empty when it cannot be extracted from Raw
	Position     string // upstream position/cursor the message belongs to
	ReceivedAt   time.Time
	Attempts     int
	NextRetryAt  *time.Time // nil for parked messages
	Status       Status
}

// ErrUnknownMessage is returned by the store when a message ID is not present.
var ErrUnknownMessage = errors.New("deadletter: unknown message")

// Store is the durable backend for dead-lettered messages. The implementation
// lands together with migration 004 and must keep the table inside the
// partition router (key message_id) so it inherits RF=2.
type Store interface {
	// Record stores a message atomically: park (retryable=false) or queue for
	// retry. Recording must not lose the payload on process restart.
	Record(ctx context.Context, message Message) error

	// List returns messages, optionally filtered by source and status.
	List(ctx context.Context, source string, status *Status) ([]Message, error)

	// Replay re-queues parked messages for reprocessing. Ordering per
	// source_ref_id is preserved by the reprocessor, not by the store.
	Replay(ctx context.Context, messageIDs []string) error

	// MarkRecovered moves a message out of the active set after a successful
	// reprocess; recovered messages disappear from /internal/audit/unprocessed.
	MarkRecovered(ctx context.Context, messageID string) error

	// ReleaseCursor reports whether parking this message may advance the
	// source cursor. P3 rule: a parked message MUST release the cursor, or a
	// single poison message stalls the whole source.
	ReleaseCursor(source string) bool
}

// Policy groups the tunables for parking behavior.
type Policy struct {
	MaxAttempts int           // attempts before a retryable message is parked
	BackoffBase time.Duration // first retry delay; doubled per attempt with jitter
}

// DefaultPolicy mirrors context-m2.md section 7.3 defaults.
func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 5, BackoffBase: 2 * time.Second}
}

// Classify maps a processing error to a canonical reason. Errors from well-
// known sentinel values should be wrapped with fmt.Errorf("%w") so classification
// stays stable across call sites.
func Classify(err error) (reason string) {
	if err == nil {
		return ""
	}
	for _, candidate := range []struct {
		target error
		reason string
	}{
		{ErrMessageTooLarge, ReasonTooLarge},
		{ErrMessageTooDeep, ReasonTooDeep},
		{ErrInvalidUTF8, ReasonInvalidUTF8},
		{ErrMalformedJSON, ReasonMalformedJSON},
		{ErrSchemaIncompatible, ReasonSchemaIncompatible},
		{ErrContractViolation, ReasonContractViolation},
	} {
		if errors.Is(err, candidate.target) {
			return candidate.reason
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonTimeout
	case errors.Is(err, context.Canceled):
		return "" // caller cancellation is not a message failure
	default:
		return ReasonStoreUnavailable
	}
}

// Sentinel errors for classification. Attach source detail with fmt.Errorf.
var (
	ErrMessageTooLarge     = errors.New("deadletter: message exceeds size limit")
	ErrMessageTooDeep      = errors.New("deadletter: message nesting exceeds limit")
	ErrInvalidUTF8         = errors.New("deadletter: message is not valid UTF-8")
	ErrMalformedJSON       = errors.New("deadletter: message is not parseable JSON")
	ErrSchemaIncompatible  = errors.New("deadletter: schema change is incompatible with the policy")
	ErrContractViolation   = errors.New("deadletter: message violates the source contract")
)

// ParkDecision is the outcome of evaluating one failed message.
type ParkDecision struct {
	Status    Status
	NextRetry *time.Time
}

// Decide applies the policy to a classified reason and attempt count.
func Decide(policy Policy, reason string, attempts int, now time.Time) ParkDecision {
	if !Retryable(reason) {
		return ParkDecision{Status: StatusParked}
	}
	if attempts >= policy.MaxAttempts {
		return ParkDecision{Status: StatusParked}
	}
	// Exponential backoff with doubling: base * 2^attempts. Jitter is added by
	// the scheduler, not the decision, so decisions stay deterministic.
	next := now.Add(policy.BackoffBase << uint(min(attempts, 20)))
	return ParkDecision{Status: StatusRetrying, NextRetry: &next}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Describe returns a compact human-readable summary for logs and the audit API.
func Describe(message Message) string {
	return fmt.Sprintf("source=%s reason=%s status=%s attempts=%d ref=%s", message.Source, message.Reason, message.Status, message.Attempts, message.SourceRefID)
}
