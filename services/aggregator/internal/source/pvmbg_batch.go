package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/deadletter"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
)

const pvmbgResponseLimit = 10 << 20 // same ceiling as the existing HTTP reader
const pvmbgCaptureLimit = 32 << 20  // bounded archival capture, never decoded as a batch
const pvmbgRecordLimit = 1 << 20    // per-message budget from the M2 working contract

// PVMBGBatch separates an unreadable response from invalid individual records.
// A truncated capture must never be treated as a complete DLQ payload.
type PVMBGBatch struct {
	Records   []PVMBGRecord
	Raw       []byte // whole response when Invalid is non-nil
	Invalid   error
	Truncated bool
}

type PVMBGRecord struct {
	Raw     []byte
	Report  domain.VolcanicReport
	Invalid error
}

// FetchVolcanicBatch preserves wire bytes for downstream validation and DLQ.
// HTTP/transport errors are returned separately from invalid successful bodies.
func (c *PVMBGClient) FetchVolcanicBatch(ctx context.Context, since time.Time, correlationID string) (PVMBGBatch, error) {
	var batch PVMBGBatch
	err := (jsonGetter{c.baseURL, c.client, c.logger, "pvmbg"}).getBody(ctx, "/volcanic-reports", since, correlationID, "Authorization", "Bearer "+c.token, func(body io.Reader) error {
		raw, err := io.ReadAll(io.LimitReader(body, pvmbgCaptureLimit+1))
		if err != nil {
			return err
		}
		if len(raw) > pvmbgCaptureLimit {
			batch = PVMBGBatch{Invalid: deadletter.ErrMessageTooLarge, Truncated: true}
		} else if len(raw) > pvmbgResponseLimit {
			batch = PVMBGBatch{Raw: raw, Invalid: deadletter.ErrMessageTooLarge}
		} else {
			batch = parsePVMBGBatch(raw)
		}
		return nil
	})
	return batch, err
}

func parsePVMBGBatch(raw []byte) PVMBGBatch {
	// Scan item boundaries independently of encoding/json's nesting limit.
	// This lets a deeply nested item be quarantined without losing its siblings.
	// The regular JSON decoder still validates each candidate item.
	bad := PVMBGBatch{Raw: raw, Invalid: deadletter.ErrMalformedJSON}
	body := trimJSONSpace(raw)
	if len(body) < 2 || body[0] != '[' || body[len(body)-1] != ']' {
		return bad
	}
	items := body[1 : len(body)-1]
	if len(trimJSONSpace(items)) == 0 {
		return PVMBGBatch{}
	}
	var batch PVMBGBatch
	start, depth, peak := 0, 0, 0
	var quoted, escaped bool
	appendItem := func(end int) bool {
		item := trimJSONSpace(items[start:end])
		if len(item) == 0 {
			return false
		}
		record := PVMBGRecord{Raw: item}
		switch {
		case len(item) > pvmbgRecordLimit:
			record.Invalid = deadletter.ErrMessageTooLarge
		case peak > 10000: // encoding/json cannot decode these items
			record.Invalid = deadletter.ErrMessageTooDeep
		case !json.Valid(item):
			return false
		case !utf8.Valid(item):
			record.Invalid = deadletter.ErrInvalidUTF8
		case item[0] != '{':
			record.Invalid = deadletter.ErrContractViolation
		default:
			var identity struct {
				ReportID string `json:"report_id"`
			}
			_ = json.Unmarshal(item, &identity)
			if err := json.Unmarshal(item, &record.Report); err != nil {
				record.Report.ReportID = identity.ReportID
				record.Invalid = errors.Join(deadletter.ErrSchemaIncompatible, err)
			} else if report := record.Report; report.ReportID == "" || report.VolcanoID == "" ||
				report.AlertLevel == "" || report.ReportedAt.IsZero() {
				record.Invalid = deadletter.ErrSchemaIncompatible
			}
		}
		batch.Records = append(batch.Records, record)
		return true
	}
	for i, ch := range items {
		if quoted {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				quoted = false
			}
			continue
		}
		switch ch {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			peak = max(peak, depth)
		case '}', ']':
			depth--
			if depth < 0 {
				return bad
			}
		case ',':
			if depth == 0 {
				if !appendItem(i) {
					return bad
				}
				start, peak = i+1, 0
			}
		}
	}
	if quoted || depth != 0 || !appendItem(len(items)) {
		return bad
	}
	return batch
}

// DecodePVMBGReplay accepts one stored report. Known migrations are supported
// only during recovery; normal ingestion keeps detecting incompatible inputs.
func DecodePVMBGReplay(raw []byte) PVMBGRecord {
	if len(raw) > pvmbgRecordLimit {
		return PVMBGRecord{Invalid: deadletter.ErrMessageTooLarge}
	}
	wrapped := append(append([]byte{'['}, raw...), ']')
	batch := parsePVMBGBatch(wrapped)
	if batch.Invalid != nil || len(batch.Records) != 1 {
		return PVMBGRecord{Invalid: deadletter.ErrMalformedJSON}
	}
	record := batch.Records[0]
	if !errors.Is(record.Invalid, deadletter.ErrSchemaIncompatible) {
		return record
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return record
	}
	changed := false
	if fields["alert_level"] == nil && fields["status"] != nil {
		fields["alert_level"] = fields["status"]
		delete(fields, "status")
		changed = true
	}
	if ts := fields["reported_at"]; len(ts) > 0 && ts[0] >= '0' && ts[0] <= '9' {
		var seconds int64
		if json.Unmarshal(ts, &seconds) == nil && seconds > 0 && seconds <= 253402300799 {
			fields["reported_at"], _ = time.Unix(seconds, 0).UTC().MarshalJSON()
			changed = true
		}
	}
	if !changed {
		return record
	}
	adapted, err := json.Marshal(fields)
	if err != nil {
		return record
	}
	batch = parsePVMBGBatch(append(append([]byte{'['}, adapted...), ']'))
	if batch.Invalid == nil && len(batch.Records) == 1 {
		return batch.Records[0]
	}
	return record
}

func trimJSONSpace(raw []byte) []byte { return bytes.Trim(raw, " \t\r\n") }
