// Package reporting is the Field Team report processor (D-FR): a competing
// consumer on the "field-reports" quorum queue running inside every Aggregator
// replica. Guarantee chain: at-least-once delivery + idempotent insert on a
// unique report_id = effectively-once processing of each accepted report.
//
// STATUS: skeleton (17 Nov 2026). The durable effect (ReportStore) awaits
// member C's field_reports migration; wiring into cmd/server/main.go starts
// only after a real store exists, because consuming without a durable effect
// and acking would drop reports.
package reporting

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// QueueName matches the queue the Client API publisher declares (D-FR).
const QueueName = "field-reports"

// maxDeliveryAttempts mirrors the DLQ retry policy (DefaultPolicy MaxAttempts)
// for now: a quorum queue redelivers with x-delivery-count, and beyond this
// the report is parked (logged loudly, acked) instead of looping forever.
// Parking will move to the unprocessed_messages table once D-DLQ lands.
const maxDeliveryAttempts = 5

// Report mirrors the payload published by client-api's reportqueue package.
type Report struct {
	ReportID   string     `json:"report_id"`
	HazardID   string     `json:"hazard_id,omitempty"`
	Note       string     `json:"note"`
	AreaName   string     `json:"area_name,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
	Reporter   string     `json:"reporter"`
	ReceivedAt time.Time  `json:"received_at"`
}

var reportIDFormat = func(id string) bool {
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		return false
	}
	for _, part := range []int{8, 4, 4, 4, 12} {
		if len(parts[0]) != part {
			return false
		}
		parts = parts[1:]
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || r == '-') {
			return false
		}
	}
	return len(id) == 36
}

// parseReport is strict: report_id and note are mandatory, received_at must be
// present, and unknown fields are rejected so schema drift upstream fails loud
// instead of silently losing data.
func parseReport(payload []byte) (Report, error) {
	var report Report
	if len(payload) == 0 {
		return Report{}, errors.New("empty field report payload")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("decode field report: %w", err)
	}
	if !reportIDFormat(report.ReportID) {
		return Report{}, errors.New("field report is missing a valid report_id")
	}
	if strings.TrimSpace(report.Note) == "" {
		return Report{}, errors.New("field report is missing note")
	}
	if report.ReceivedAt.IsZero() {
		return Report{}, errors.New("field report is missing received_at")
	}
	return report, nil
}
