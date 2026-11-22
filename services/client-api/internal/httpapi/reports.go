package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/reportqueue"
)

// reportSink accepts an already validated report; its contract (D-FR) is to
// return only after the broker durably confirmed the message, so the handler
// may answer 202 Accepted.
type reportSink interface {
	Publish(ctx context.Context, report reportqueue.Report, correlationID string) error
}

const (
	reportNoteMax     = 4000
	reportAreaMax     = 200
	reportHazardMax   = 128
	reportBodyMax     = 32 * 1024
)

var reportIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var hazardIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type fieldReportInput struct {
	ReportID   string  `json:"report_id"`
	HazardID   string  `json:"hazard_id"`
	Note       string  `json:"note"`
	AreaName   string  `json:"area_name"`
	OccurredAt *string `json:"occurred_at"`
}

func serveFieldReports(admit *admission, validator accessValidator, sink reportSink) http.HandlerFunc {
	return limitRequests(admit, authenticate(validator, func(w http.ResponseWriter, r *http.Request) {
		info := r.Context().Value(identityKey{}).(*requestIdentity)
		// Only the Field Team writes reports; Media and Internal Ops have no
		// submit authority on this path.
		if info.Scope != "field-team" {
			writeError(w, http.StatusForbidden, "forbidden", "field reports are submitted by the Field Team only")
			return
		}
		if sink == nil {
			writeError(w, http.StatusServiceUnavailable, "broker_unavailable", "report ingestion unavailable")
			return
		}
		id, _ := r.Context().Value(correlationKey{}).(string)
		report, bad := parseFieldReport(r, id)
		if bad != nil {
			bad.reply(w)
			return
		}
		report.Reporter = info.ClientID
		if err := sink.Publish(r.Context(), report, id); err != nil {
			// D-FR: no confirm, no 202. A lost confirm must surface as 503 so
			// the Field Team client retries instead of assuming acceptance.
			writeError(w, http.StatusServiceUnavailable, "broker_unavailable", "report could not be durably accepted")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"report_id":   report.ReportID,
			"status":      "accepted",
			"received_at": report.ReceivedAt.Format(time.RFC3339Nano),
		})
	}))
}

type inputError struct {
	status  int
	code    string
	message string
}

func (e *inputError) reply(w http.ResponseWriter) {
	writeError(w, e.status, e.code, e.message)
}

// parseFieldReport enforces the validated payload before anything durable
// happens: idempotency key from the body or the Idempotency-Key header, UTF-8
// bodies, bounded fields, and an optional RFC 3339 occurred_at.
func parseFieldReport(r *http.Request, correlationID string) (reportqueue.Report, *inputError) {
	body, err := readCappedBody(r, reportBodyMax)
	if err != nil {
		return reportqueue.Report{}, &inputError{http.StatusBadRequest, "invalid_body", "invalid field report body"}
	}
	var input fieldReportInput
	if len(body) > 0 {
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return reportqueue.Report{}, &inputError{http.StatusBadRequest, "invalid_body", "invalid field report body"}
		}
	}
	reportID := strings.TrimSpace(input.ReportID)
	headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if reportID == "" {
		reportID = headerKey
	} else if headerKey != "" && headerKey != reportID {
		return reportqueue.Report{}, &inputError{http.StatusBadRequest, "idempotency_conflict", "Idempotency-Key header does not match report_id"}
	}
	if !reportIDPattern.MatchString(reportID) {
		return reportqueue.Report{}, &inputError{http.StatusBadRequest, "invalid_report_id", "report_id must be a UUID (or set the Idempotency-Key header)"}
	}
	note := strings.TrimSpace(input.Note)
	if note == "" {
		return reportqueue.Report{}, &inputError{http.StatusBadRequest, "invalid_note", "note is required"}
	}
	if !utf8.ValidString(note) || len(note) > reportNoteMax {
		return reportqueue.Report{}, &inputError{http.StatusBadRequest, "invalid_note", "note must be UTF-8 text of at most 4000 bytes"}
	}
	area := strings.TrimSpace(input.AreaName)
	if len(area) > reportAreaMax {
		return reportqueue.Report{}, &inputError{http.StatusBadRequest, "invalid_area_name", "area_name must be at most 200 bytes"}
	}
	hazardID := strings.TrimSpace(input.HazardID)
	if hazardID != "" && !hazardIDPattern.MatchString(hazardID) {
		return reportqueue.Report{}, &inputError{http.StatusBadRequest, "invalid_hazard_id", "hazard_id must be 1-128 letters, digits, dashes, or underscores"}
	}
	var occurred *time.Time
	if input.OccurredAt != nil && strings.TrimSpace(*input.OccurredAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*input.OccurredAt))
		if err != nil {
			return reportqueue.Report{}, &inputError{http.StatusBadRequest, "invalid_occurred_at", "occurred_at must be an RFC 3339 timestamp"}
		}
		occurred = &parsed
	}
	return reportqueue.Report{
		ReportID:   reportID,
		HazardID:   hazardID,
		Note:       note,
		AreaName:   area,
		OccurredAt: occurred,
		ReceivedAt: time.Now().UTC(),
	}, nil
}

func readCappedBody(r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}
