// Package reportqueue publishes Field Team reports to the durable "field-reports"
// quorum queue (D-FR). A publish only succeeds after the broker confirms that a
// majority of queue replicas persisted the message, which is what lets the
// Client API answer 202 Accepted without losing a report whose consumers may
// die before processing the report.
package reportqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// QueueName matches the queue declared in the broker topology.
const QueueName = "field-reports"

const dialTimeout = 5 * time.Second

var (
	// ErrUnavailable maps to 503: the broker could not durably confirm the
	// report, so no 202 may be sent (a 202 before confirm can lose reports).
	ErrUnavailable = errors.New("broker unavailable or report not confirmed")
)

// Report is the durable payload. ReportID is the client idempotency key and
// stays stable across retries of the same report; the consumer dedups on it.
type Report struct {
	ReportID   string     `json:"report_id"`
	HazardID   string     `json:"hazard_id,omitempty"`
	Note       string     `json:"note"`
	AreaName   string     `json:"area_name,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
	Reporter   string     `json:"reporter"`
	ReceivedAt time.Time  `json:"received_at"`
}

// Publisher holds one confirmed channel. Connections are re-established lazily
// after failures; each Publish either ends with a broker confirm or an error.
type Publisher struct {
	brokerURL string
	timeout   time.Duration
	logger    *slog.Logger

	mu         sync.Mutex
	connection *amqp.Connection
	channel    *amqp.Channel
	socket     net.Conn
	returns    <-chan amqp.Return
}

func New(brokerURL string, publishTimeout time.Duration, logger *slog.Logger) (*Publisher, error) {
	if _, err := amqp.ParseURI(brokerURL); err != nil {
		return nil, errors.New("invalid BROKER_URL configuration")
	}
	if publishTimeout <= 0 {
		return nil, errors.New("field report publish timeout must be positive")
	}
	return &Publisher{brokerURL: brokerURL, timeout: publishTimeout, logger: logger}, nil
}

type brokerSession struct {
	connection *amqp.Connection
	channel    *amqp.Channel
	socket     net.Conn
	returns    <-chan amqp.Return
}

func (s *brokerSession) close() {
	if s.channel != nil {
		s.channel.Close()
	}
	if s.connection != nil {
		s.connection.Close()
	}
	s.socket = nil
}

// dial opens one connection with socket deadlines that interrupt blocked I/O
// even when the library ignores context cancellation mid-frame. The whole dial
// (including DNS resolution) is bounded by dialTimeout so a broken broker
// topology surfaces as a fast 503, not a hanging request.
func (p *Publisher) dial(ctx context.Context) (*brokerSession, error) {
	session := &brokerSession{}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	connection, err := amqp.DialConfig(p.brokerURL, amqp.Config{
		Heartbeat: 5 * time.Second,
		Dial: func(network, address string) (net.Conn, error) {
			socket, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(dialCtx, network, address)
			if err == nil {
				session.socket = socket
				err = socket.SetDeadline(time.Now().Add(dialTimeout))
			}
			return socket, err
		},
	})
	if err != nil {
		session.close()
		return nil, errors.New("broker connection failed") // Never log credential-bearing URLs.
	}
	session.connection = connection
	setupCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	abort := context.AfterFunc(setupCtx, func() { session.close() })
	defer abort()
	channel, err := connection.Channel()
	if err != nil {
		session.close()
		return nil, errors.New("broker channel setup failed")
	}
	session.channel = channel
	return session, nil
}

// ensureSession returns a live session on the declared queue. The queue is
// checked passively first so a topology-declared queue keeps its arguments; a
// missing queue is then declared as a durable quorum queue per the frozen
// contract. Each attempt uses its own fresh connection so a failed passive
// declare cannot poison the publishing channel.
func (p *Publisher) ensureSession(ctx context.Context) (*brokerSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.channel != nil && !p.channel.IsClosed() {
		return &brokerSession{connection: p.connection, channel: p.channel, socket: p.socket, returns: p.returns}, nil
	}
	p.dropLocked()
	for attempt, declare := range []func(*amqp.Channel) error{
		// Passive first: adopt the queue when the topology already owns it.
		func(channel *amqp.Channel) error {
			_, err := channel.QueueDeclarePassive(QueueName, true, false, false, false, nil)
			return err
		},
		// Missing: declare the durable quorum queue (D-FR).
		func(channel *amqp.Channel) error {
			_, err := channel.QueueDeclare(QueueName, true, false, false, false,
				amqp.Table{"x-queue-type": "quorum"})
			return err
		},
	} {
		session, err := p.dial(ctx)
		if err != nil {
			return nil, err
		}
		setupCtx, cancel := context.WithTimeout(ctx, dialTimeout)
		// The timer must close the session on timeout but stay disarmed on
		// success: cancel() would fire the close callback, so success only
		// aborts the timer and keeps the session open for publishing.
		abort := context.AfterFunc(setupCtx, func() { session.close() })
		declareErr := declare(session.channel)
		confirmErr := error(nil)
		if declareErr == nil {
			confirmErr = session.channel.Confirm(false)
		}
		if declareErr == nil && confirmErr == nil && setupCtx.Err() == nil {
			session.returns = session.channel.NotifyReturn(make(chan amqp.Return, 1))
			p.connection, p.channel, p.socket, p.returns = session.connection, session.channel, session.socket, session.returns
			abort()
			return session, nil
		}
		session.close()
		abort()
		cancel()
		if p.logger != nil {
			p.logger.Warn("field-reports queue setup attempt failed",
				"service", "client-api", "target", "broker", "queue", QueueName,
				"attempt", attempt+1, "declare_failed", declareErr != nil, "setup_canceled", setupCtx.Err() != nil)
		}
	}
	return nil, errors.New("field-reports queue setup failed")
}

// Publish connects if needed, then publishes and waits for the broker
// confirmation within the configured timeout. The socket is closed on
// cancellation so library calls cannot block past the context deadline.
func (p *Publisher) Publish(ctx context.Context, report Report, correlationID string) error {
	session, err := p.ensureSession(ctx)
	if err != nil {
		return ErrUnavailable
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	publishCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	abort := context.AfterFunc(publishCtx, func() { session.socket.Close() })
	defer abort()
	confirmation, err := session.channel.PublishWithDeferredConfirmWithContext(publishCtx, "", QueueName, true, false, amqp.Publishing{
		ContentType:   "application/json",
		DeliveryMode:  amqp.Persistent,
		MessageId:     report.ReportID,
		CorrelationId: correlationID,
		Timestamp:     report.ReceivedAt,
		Type:          "FieldReport",
		Body:          payload,
		Headers:       amqp.Table{"X-Correlation-ID": correlationID},
	})
	if err != nil {
		p.drop()
		p.logPublishFailure(correlationID, "publish_write_failed")
		return ErrUnavailable
	}
	ack, err := confirmation.WaitContext(publishCtx)
	if err != nil || !ack {
		p.drop()
		p.logPublishFailure(correlationID, "confirm_missing_or_negative")
		return ErrUnavailable
	}
	select {
	case returned, ok := <-session.returns:
		if !ok {
			p.drop()
			return ErrUnavailable
		}
		if p.logger != nil {
			p.logger.Warn("field report publish returned undeliverable",
				"service", "client-api", "target", "broker", "queue", QueueName,
				"correlation_id", correlationID, "reply_code", returned.ReplyCode)
		}
		p.drop()
		return ErrUnavailable
	default:
	}
	return nil
}

// logPublishFailure records why a durable publish did not happen; messages
// carry no payload or credential material.
func (p *Publisher) logPublishFailure(correlationID, reason string) {
	if p.logger != nil {
		p.logger.Warn("field report publish failed",
			"service", "client-api", "target", "broker", "queue", QueueName,
			"correlation_id", correlationID, "reason", reason)
	}
}

func (p *Publisher) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropLocked()
}

func (p *Publisher) dropLocked() {
	if p.channel != nil {
		p.channel.Close()
		p.channel = nil
	}
	if p.socket != nil {
		p.socket.Close()
		p.socket = nil
	}
	if p.connection != nil {
		p.connection.Close()
		p.connection = nil
	}
}

// Close releases the broker connection at shutdown.
func (p *Publisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropLocked()
}
