package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

func TestOutboxPublisher(t *testing.T) {
	brokerURL, databaseURL := os.Getenv("TEST_BROKER_URL"), os.Getenv("TEST_DATABASE_URL")
	if brokerURL == "" || databaseURL == "" {
		t.Skip("set TEST_BROKER_URL and TEST_DATABASE_URL for real broker/storage checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	schema := fmt.Sprintf("publisher_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	endpoint, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := endpoint.Query()
	query.Set("search_path", schema)
	endpoint.RawQuery = query.Encode()
	repo, err := store.OpenPostgres(ctx, endpoint.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	// Test-owned schema, exchange, and queue leave canonical data/base queues untouched.
	p, err := connect(ctx, brokerURL, "hazard.events")
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if err := p.channel.ExchangeDeclare(schema, "fanout", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	defer p.channel.ExchangeDelete(schema, false, false)
	queue, err := p.channel.QueueDeclare(schema, true, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.channel.QueueDelete(queue.Name, false, false, false)
	if err := p.channel.QueueBind(queue.Name, "", schema, false, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	event := domain.MapSeismic(domain.SeismicEvent{EventID: schema, RegionName: "Publisher integration check",
		Magnitude: 5, EpicenterLat: -7, EpicenterLon: 110, OccurredAt: now}, nil, now)
	if _, err := repo.Upsert(ctx, []domain.HazardEvent{event}, schema); err != nil {
		t.Fatal(err)
	}
	readPending := func(want int) []store.OutboxMessage {
		t.Helper()
		messages, err := repo.PendingOutbox(ctx)
		if err != nil || len(messages) != want {
			t.Fatalf("pending=%d want=%d err=%v", len(messages), want, err)
		}
		return messages
	}
	message := readPending(1)[0]
	// Publish cancelled before confirmation cannot consume or mark the pending row.
	cancelled, stop := context.WithCancel(ctx)
	stop()
	cancelPublisher, err := connect(ctx, brokerURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := cancelPublisher.publish(cancelled, message); err == nil {
		t.Fatal("cancelled publication must fail")
	}
	cancelPublisher.close()
	readPending(1)
	// Simulate the crash window: broker accepted the message but published_at was never written.
	p.exchange = schema
	if err := p.publish(ctx, message); err != nil {
		t.Fatal(err)
	}
	readPending(1)
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); Run(workerCtx, repo, brokerURL, schema, logger) }()
	defer func() { stopWorker(); <-done }()
	delivered := 0
	for time.Now().Before(now.Add(10 * time.Second)) {
		delivery, ok, err := p.channel.Get(queue.Name, false)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			var body domain.HazardEvent
			if err := json.Unmarshal(delivery.Body, &body); err != nil {
				t.Fatal(err)
			}
			if delivery.MessageId != strconv.FormatInt(message.MessageID, 10) || body.HazardID != event.HazardID ||
				delivery.CorrelationId != schema || delivery.Headers["X-Correlation-ID"] != schema ||
				delivery.Headers["schema_version"] != int32(1) || delivery.Headers["hazard_revision"] != int64(1) || delivery.DeliveryMode != 2 {
				t.Fatal("persistent message body/identity/correlation/revision mismatch")
			}
			if err := delivery.Ack(false); err != nil {
				t.Fatal(err)
			}
			delivered++
			if delivered == 2 {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if delivered != 2 {
		t.Fatal("accepted-but-unmarked message must be retried with the same ID/body")
	}
	stopWorker()
	<-done
	readPending(0)
	// An isolated exchange without bindings must return mandatory messages, even with positive ack.
	unrouted := schema + "_unrouted"
	if err := p.channel.ExchangeDeclare(unrouted, "fanout", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	defer p.channel.ExchangeDelete(unrouted, false, false)
	badRoute, err := connect(ctx, brokerURL, unrouted)
	if err != nil {
		t.Fatal(err)
	}
	defer badRoute.close()
	for range 10 { // Repeat to catch return/confirm ordering mistakes.
		if err := badRoute.publish(ctx, message); err == nil {
			t.Fatal("unroutable mandatory message must not be treated as sent")
		}
	}
	t.Log("PASS: real worker drains outbox after confirm; persistent body/IDs/headers match; cancellation leaves pending; accepted-but-unmarked retry duplicates the same ID; mandatory return is rejected")
}
