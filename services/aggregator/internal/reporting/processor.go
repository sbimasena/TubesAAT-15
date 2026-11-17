package reporting

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const dialTimeout = 5 * time.Second

// Run consumes the field-reports queue until ctx is done. One instance per
// Aggregator replica: quorum queue competing consumers, so every replica may
// receive a different report. Redelivery on failure keeps reports safe.
func Run(ctx context.Context, brokerURL string, store ReportStore, logger *slog.Logger) {
	if store == nil || logger == nil {
		return
	}
	if _, err := amqp.ParseURI(brokerURL); err != nil {
		logger.Error("field report processor cannot start", "service", "aggregator",
			"queue", QueueName, "error", "invalid BROKER_URL configuration")
		return
	}
	for {
		session, declared, err := openSession(ctx, brokerURL)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("field report processor reconnecting", "service", "aggregator",
				"queue", QueueName, "error", err.Error())
			if !sleepContext(ctx, 2*time.Second) {
				return
			}
			continue
		}
		logger.Info("field report processor consuming", "service", "aggregator",
			"queue", QueueName, "queue_declared", declared)
		err = consume(ctx, session, store, logger)
		session.close()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.Warn("field report processor restarting after channel failure",
				"service", "aggregator", "queue", QueueName, "error", err.Error())
		}
		if !sleepContext(ctx, time.Second) {
			return
		}
	}
}

// openSession tries to adopt the queue passively first; a missing queue gets
// exactly one active declaration with the frozen quorum arguments, so this
// processor and the Client API publisher always agree on the topology.
func openSession(ctx context.Context, brokerURL string) (*session, bool, error) {
	session, err := dialAndSetup(ctx, brokerURL, false)
	if err == nil {
		return session, false, nil
	}
	session, err = dialAndSetup(ctx, brokerURL, true)
	if err != nil {
		return nil, false, err
	}
	return session, true, nil
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// consume runs one channel lifetime; every failure returns and the outer loop
// reconnects with a fresh session.
func consume(ctx context.Context, session *session, store ReportStore, logger *slog.Logger) error {
	if err := session.channel.Qos(1, 0, false); err != nil {
		return err
	}
	messages, err := session.channel.ConsumeWithContext(ctx, QueueName, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case message, ok := <-messages:
			if !ok {
				return errors.New("field-reports consume channel closed")
			}
			handle(ctx, logger, store, adapter{delivery: message, ctx: ctx})
		}
	}
}

// session owns one connection plus its channel; a failed setup closes both.
type session struct {
	connection *amqp.Connection
	channel    *amqp.Channel
	socket     net.Conn
}

func (s *session) close() {
	if s.channel != nil {
		s.channel.Close()
	}
	if s.connection != nil {
		s.connection.Close()
	}
	s.socket = nil
}

// dialAndSetup opens one connection and channel, then declares the queue
// passively (adopt topology) or actively (frozen quorum arguments).
func dialAndSetup(ctx context.Context, brokerURL string, declareActive bool) (*session, error) {
	s := &session{}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	connection, err := amqp.DialConfig(brokerURL, amqp.Config{
		Heartbeat: 5 * time.Second,
		Dial: func(network, address string) (net.Conn, error) {
			socket, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(dialCtx, network, address)
			if err == nil {
				s.socket = socket
				err = socket.SetDeadline(time.Now().Add(dialTimeout))
			}
			return socket, err
		},
	})
	if err != nil {
		s.close()
		return nil, errors.New("broker connection failed") // Never log credential-bearing URLs.
	}
	s.connection = connection
	setupCtx, cancel := context.WithTimeout(dialCtx, dialTimeout)
	defer cancel()
	// The timer closes the session on timeout; on success the timer must be
	// disarmed WITHOUT canceling the context, or the session dies at setup.
	abort := context.AfterFunc(setupCtx, func() { s.close() })
	channel, err := connection.Channel()
	if err == nil {
		s.channel = channel
		if declareActive {
			_, err = s.channel.QueueDeclare(QueueName, true, false, false, false,
				amqp.Table{"x-queue-type": "quorum"})
		} else {
			_, err = s.channel.QueueDeclarePassive(QueueName, true, false, false, false, nil)
		}
	}
	if err == nil {
		err = s.channel.Qos(1, 0, false)
	}
	if err != nil || setupCtx.Err() != nil {
		s.close()
		abort()
		cancel()
		return nil, errors.New("field-reports queue setup failed")
	}
	abort()
	return s, nil
}

// adapter bridges amqp.Delivery to the testable delivery interface.
type adapter struct {
	delivery amqp.Delivery
	ctx      context.Context
}

func (a adapter) Body() []byte          { return a.delivery.Body }
func (a adapter) CorrelationID() string { return a.delivery.CorrelationId }
func (a adapter) DeliveryCount() int {
	if count, ok := a.delivery.Headers["x-delivery-count"].(int32); ok {
		return int(count)
	}
	return 0
}
func (a adapter) Ack(ctx context.Context) error { return a.delivery.Ack(false) }
func (a adapter) Nack(ctx context.Context, requeue bool) error {
	return a.delivery.Nack(false, requeue)
}
