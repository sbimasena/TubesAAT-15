package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const maxBody = 1 << 20

type hazard struct {
	HazardID   string `json:"hazard_id"`
	Source     string `json:"source"`
	HazardType string `json:"hazard_type"`
	Severity   string `json:"severity"`
	AreaName   string `json:"area_name"`
}

type record struct {
	MessageID     string          `json:"message_id"`
	HazardID      string          `json:"hazard_id"`
	Revision      int64           `json:"hazard_revision"`
	CorrelationID string          `json:"correlation_id"`
	Payload       json.RawMessage `json:"payload"`
	Result        string          `json:"result"`
	ReceivedAt    time.Time       `json:"received_at"`
}

type journal struct {
	file   *os.File
	seen   map[string]bool
	latest map[string]record
	// ponytail: in-memory ID index grows with the JSONL journal; compact/use a consumer DB beyond M1.
}

func integer(value any) int64 {
	switch number := value.(type) {
	case int32:
		return int64(number)
	case int64:
		return number
	default:
		return 0
	}
}

func decode(delivery amqp.Delivery) (record, error) {
	r := record{MessageID: delivery.MessageId, Revision: integer(delivery.Headers["hazard_revision"]),
		CorrelationID: delivery.CorrelationId, Payload: delivery.Body, ReceivedAt: time.Now().UTC()}
	id, err := strconv.ParseInt(r.MessageID, 10, 64)
	if err != nil || id <= 0 || len(r.CorrelationID) == 0 || len(r.CorrelationID) > 128 ||
		delivery.Headers["X-Correlation-ID"] != r.CorrelationID || integer(delivery.Headers["schema_version"]) != 1 ||
		r.Revision <= 0 || delivery.ContentType != "application/json" || len(r.Payload) > maxBody {
		return r, errors.New("invalid HazardEvent metadata")
	}
	var body hazard
	if err := json.Unmarshal(r.Payload, &body); err != nil {
		return r, errors.New("invalid HazardEvent JSON")
	}
	if body.HazardID == "" || len(body.HazardID) > 128 || body.AreaName == "" ||
		!((body.Source == "BMKG" && body.HazardType == "SEISMIC") || (body.Source == "PVMBG" && body.HazardType == "VOLCANIC")) {
		return r, errors.New("invalid canonical identity/source/type/area")
	}
	switch body.Severity {
	case "NORMAL", "WASPADA", "SIAGA", "AWAS":
	default:
		return r, errors.New("invalid severity")
	}
	r.HazardID = body.HazardID
	return r, nil
}

func openJournal(path string) (*journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	j := &journal{file: file, seen: make(map[string]bool), latest: make(map[string]record)}
	fail := func(err error) (*journal, error) { file.Close(); return nil, err }
	// One writer per volume, including accidental additional container replicas.
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(err)
	}
	reader := bufio.NewReaderSize(file, 2*maxBody)
	var offset int64
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) {
			// A newline is the commit boundary; a torn final record was never acknowledged.
			if len(line) != 0 {
				if err := file.Truncate(offset); err != nil {
					return fail(err)
				}
			}
			break
		}
		if err != nil {
			return fail(fmt.Errorf("read journal at offset %d: %w", offset, err))
		}
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			return fail(fmt.Errorf("corrupt journal at offset %d", offset))
		}
		if r.MessageID == "" || r.HazardID == "" || r.Revision <= 0 || r.CorrelationID == "" || r.ReceivedAt.IsZero() ||
			!json.Valid(r.Payload) || (r.Result != "dashboard_updated" && r.Result != "stale_ignored") || j.seen[r.MessageID] {
			return fail(fmt.Errorf("invalid journal record at offset %d", offset))
		}
		j.restore(r)
		offset += int64(len(line))
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return fail(err)
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fail(err)
	}
	err = directory.Sync()
	directory.Close()
	if err != nil {
		return fail(err)
	}
	return j, nil
}

func (j *journal) restore(r record) {
	j.seen[r.MessageID] = true
	if r.Result == "dashboard_updated" && r.Revision > j.latest[r.HazardID].Revision {
		j.latest[r.HazardID] = r
	}
}

func (j *journal) save(r record) (string, error) {
	if j.seen[r.MessageID] {
		return "duplicate", nil
	}
	r.Result = "dashboard_updated"
	if r.Revision <= j.latest[r.HazardID].Revision {
		r.Result = "stale_ignored"
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(r); err != nil {
		return "", err
	}
	body := encoded.Bytes()
	n, err := j.file.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.file.Sync()
	}
	if err != nil {
		return "", err
	} // Caller closes/reopens journal before any further append.
	j.restore(r)
	return r.Result, nil
}
