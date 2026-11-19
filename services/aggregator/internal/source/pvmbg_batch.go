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
		raw, err := io.ReadAll(io.LimitReader(body, pvmbgResponseLimit+1))
		if err != nil {
			return err
		}
		if len(raw) > pvmbgResponseLimit {
			batch = PVMBGBatch{Raw: raw[:pvmbgResponseLimit], Invalid: deadletter.ErrMessageTooLarge, Truncated: true}
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
			if err := json.Unmarshal(item, &record.Report); err != nil {
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

func trimJSONSpace(raw []byte) []byte { return bytes.Trim(raw, " \t\r\n") }
