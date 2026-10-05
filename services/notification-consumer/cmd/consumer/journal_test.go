package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

type acknowledgement struct {
	acked     int
	rejected  int
	beforeAck func() error
}

func (a *acknowledgement) Ack(_ uint64, multiple bool) error {
	if multiple {
		return errors.New("unexpected multiple ack")
	}
	if a.beforeAck != nil {
		if err := a.beforeAck(); err != nil {
			return err
		}
	}
	a.acked++
	return nil
}
func (a *acknowledgement) Nack(_ uint64, _, _ bool) error { return errors.New("unexpected nack") }
func (a *acknowledgement) Reject(_ uint64, requeue bool) error {
	if requeue {
		return errors.New("malformed event must not requeue")
	}
	a.rejected++
	return nil
}
func message(id string, revision int64, a *acknowledgement) amqp.Delivery {
	return amqp.Delivery{MessageId: id, CorrelationId: "check-consumer", ContentType: "application/json",
		Headers:     amqp.Table{"X-Correlation-ID": "check-consumer", "schema_version": int32(1), "hazard_revision": revision},
		Body:        []byte(`{"hazard_id":"haz-check","source":"BMKG","hazard_type":"SEISMIC","severity":"SIAGA","area_name":"Check area","attributes":{"future_field":true}}`),
		DeliveryTag: 1, Acknowledger: a}
}
func TestJournalAndAcknowledgement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { j.file.Close() }()
	if second, err := openJournal(path); err == nil {
		second.file.Close()
		t.Fatal("second writer must be refused")
	}
	first := &acknowledgement{beforeAck: func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.HasSuffix(data, []byte{'\n'}) || !json.Valid(bytes.TrimSpace(data)) {
			return errors.New("ack before journal commit")
		}
		return nil
	}}
	delivery := message("101", 2, first)
	if err := handle(delivery, j, logger); err != nil {
		t.Fatal(err)
	}
	if first.acked != 1 || len(j.seen) != 1 {
		t.Fatal("valid event must be persisted before ack")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate commit-before-ack crash: durable append exists, ack itself fails.
	afterCommit := &acknowledgement{beforeAck: func() error { return errors.New("connection lost before ack") }}
	if err := handle(message("102", 3, afterCommit), j, logger); err == nil {
		t.Fatal("ack error must propagate")
	}
	j.file.Close()
	j, err = openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	replay := &acknowledgement{}
	repeated := message("102", 3, replay)
	repeated.Redelivered = true
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle(repeated, j, logger); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || replay.acked != 1 {
		t.Fatal("redelivery after restart must ack without repeating result", err)
	}
	if len(j.seen) != 2 || bytes.Equal(original, after) {
		t.Fatal("new message ID for same hazard must be processed")
	}
	// Distinct older revision is audited, and cannot regress dashboard state.
	older := &acknowledgement{}
	if err := handle(message("103", 1, older), j, logger); err != nil {
		t.Fatal(err)
	}
	if len(j.seen) != 3 || older.acked != 1 {
		t.Fatal("older revision must still be recorded/acked")
	}
	j.file.Close()
	// Recover a torn final record, then append without corrupting subsequent JSONL.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(`{"message_id":"torn`)
	file.Close()
	j, err = openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle(message("104", 4, &acknowledgement{}), j, logger); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("torn")) || bytes.Count(content, []byte{'\n'}) != 4 {
		t.Fatal("torn tail must be removed before append")
	}
	// Invalid metadata/JSON are rejected without journal effects, including unsupported versions.
	for _, broken := range []amqp.Delivery{message("", 1, &acknowledgement{}), message("105", 0, &acknowledgement{})} {
		if err := handle(broken, j, logger); err != nil {
			t.Fatal(err)
		}
		if broken.Acknowledger.(*acknowledgement).rejected != 1 {
			t.Fatal("invalid metadata must be rejected")
		}
	}
	broken := message("105", 1, &acknowledgement{})
	broken.Body = []byte("{broken")
	if err := handle(broken, j, logger); err != nil || broken.Acknowledger.(*acknowledgement).rejected != 1 {
		t.Fatal("invalid JSON must be rejected", err)
	}
	broken = message("105", 1, &acknowledgement{})
	broken.Headers["schema_version"] = int32(2)
	if err := handle(broken, j, logger); err != nil || broken.Acknowledger.(*acknowledgement).rejected != 1 {
		t.Fatal("unsupported schema must be rejected", err)
	}
	// Retained HTML-like attribute text must not expand past the replay line bound.
	large := message("106", 6, &acknowledgement{})
	text := append([]byte{'"'}, bytes.Repeat([]byte{'<'}, maxBody/2)...)
	text = append(text, '"')
	large.Body = bytes.Replace(large.Body, []byte("true"), text, 1)
	if err := handle(large, j, logger); err != nil {
		t.Fatal(err)
	}
	j.file.Close()
	j, err = openJournal(path)
	if err != nil || !j.seen["106"] {
		t.Fatal("large retained attributes must survive replay", err)
	}
	// IO failure must not ack or update the deduplication index.
	j.file.Close()
	failed := &acknowledgement{}
	if err := handle(message("105", 5, failed), j, logger); err == nil || failed.acked != 0 || j.seen["105"] {
		t.Fatal("journal failure must leave the event unacknowledged")
	}
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString("{corrupt}\n")
	file.Close()
	if bad, err := openJournal(path); err == nil {
		bad.file.Close()
		t.Fatal("newline-terminated journal corruption must fail explicitly")
	}
	t.Log("PASS: durable result before ack, single writer, new-ID update, restart dedup after lost ack, torn-tail recovery, invalid-message rejection, IO failure without ack, explicit corruption failure")
}
