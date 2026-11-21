package reportqueue

import (
	"context"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Use an isolated broker or vhost: this test publishes two durable reports.
func TestPublisherKeepsSessionAfterSetupContextCleanup(t *testing.T) {
	brokerURL := os.Getenv("TEST_FIELD_REPORT_BROKER_URL")
	if brokerURL == "" {
		t.Skip("set TEST_FIELD_REPORT_BROKER_URL to an isolated RabbitMQ broker")
	}
	publisher, err := New(brokerURL, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(publisher.Close)

	firstContext, cancelFirst := context.WithCancel(context.Background())
	t.Cleanup(cancelFirst)
	first := Report{
		ReportID:   "e08c7732-3fef-4eb0-ae06-57ab68e1bcd1",
		Note:       "publisher setup cleanup regression: first report",
		Reporter:   "field-team",
		ReceivedAt: time.Now().UTC(),
	}
	if err := publisher.Publish(firstContext, first, "publisher-context-first"); err != nil {
		t.Fatalf("first confirmed publish: %v", err)
	}
	connection := publisher.connection
	closed := connection.NotifyClose(make(chan *amqp.Error, 1))
	cancelFirst()

	// Neither request cancellation nor the old setup deadline may close a
	// session whose setup finished successfully and disarmed its callback.
	timer := time.NewTimer(dialTimeout + 250*time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-closed:
		t.Fatalf("successful setup cleanup closed the broker connection: %v", err)
	case <-timer.C:
	}

	second := first
	second.ReportID = "970cd45e-64fb-407f-bd76-c8af091b1eac"
	second.Note = "publisher setup cleanup regression: second report"
	second.ReceivedAt = time.Now().UTC()
	if err := publisher.Publish(context.Background(), second, "publisher-context-second"); err != nil {
		t.Fatalf("second confirmed publish: %v", err)
	}
	if publisher.connection != connection {
		t.Fatal("publisher reconnected instead of reusing the successful session")
	}
}
