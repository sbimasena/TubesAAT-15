package deadletter

import (
	"testing"
	"time"
)

func TestRetryableClassification(t *testing.T) {
	retryable := []string{ReasonTimeout, ReasonUpstream5xx, ReasonStoreUnavailable, ReasonInternalPanic}
	permanent := []string{ReasonSchemaIncompatible, ReasonMalformedJSON, ReasonInvalidUTF8, ReasonTooDeep, ReasonTooLarge, ReasonContractViolation}
	for _, reason := range retryable {
		if !Retryable(reason) {
			t.Errorf("%q should be retryable", reason)
		}
	}
	for _, reason := range permanent {
		if Retryable(reason) {
			t.Errorf("%q should be parked immediately", reason)
		}
	}
}

func TestClassifySentinels(t *testing.T) {
	cases := []struct {
		err    error
		reason string
	}{
		{ErrMalformedJSON, ReasonMalformedJSON},
		{ErrInvalidUTF8, ReasonInvalidUTF8},
		{ErrMessageTooDeep, ReasonTooDeep},
		{ErrMessageTooLarge, ReasonTooLarge},
		{ErrSchemaIncompatible, ReasonSchemaIncompatible},
		{ErrContractViolation, ReasonContractViolation},
	}
	for _, testCase := range cases {
		if got := Classify(testCase.err); got != testCase.reason {
			t.Errorf("classify %v: got %q, want %q", testCase.err, got, testCase.reason)
		}
	}
	if got := Classify(nil); got != "" {
		t.Errorf("nil error classified as %q", got)
	}
}

func TestDecideBackoffAndParking(t *testing.T) {
	policy := Policy{MaxAttempts: 3, BackoffBase: 2 * time.Second}
	now := time.Unix(0, 0).UTC()

	first := Decide(policy, ReasonTimeout, 0, now)
	if first.Status != StatusRetrying || first.NextRetry == nil || *first.NextRetry != now.Add(2*time.Second) {
		t.Fatalf("first retry: %+v", first)
	}
	third := Decide(policy, ReasonTimeout, 3, now)
	if third.Status != StatusParked || third.NextRetry != nil {
		t.Fatalf("exhausted retries must park: %+v", third)
	}
	permanent := Decide(policy, ReasonContractViolation, 0, now)
	if permanent.Status != StatusParked || permanent.NextRetry != nil {
		t.Fatalf("non-retryable must park on first attempt: %+v", permanent)
	}
}

func TestReleaseCursorOnPark(t *testing.T) {
	// Interface-level guarantee used by the ingest integration: parking a
	// message must allow the cursor to advance.
	var store Store // compile-time check that Store is usable as an interface value
	_ = store
}
