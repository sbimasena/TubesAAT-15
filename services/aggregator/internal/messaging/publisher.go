package messaging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

const brokerTimeout = 5 * time.Second

type publisher struct {
	connection *amqp.Connection
	channel    *amqp.Channel
	socket     net.Conn
	returns    chan amqp.Return
	exchange   string
}

func connect(ctx context.Context, brokerURL, exchange string) (*publisher, error) {
	if _, err := amqp.ParseURI(brokerURL); err != nil {
		return nil, errors.New("invalid BROKER_URL configuration")
	}
	p := &publisher{exchange: exchange}
	connection, err := amqp.DialConfig(brokerURL, amqp.Config{
		Heartbeat: 5 * time.Second,
		Dial: func(network, address string) (net.Conn, error) {
			socket, err := (&net.Dialer{Timeout: brokerTimeout}).DialContext(ctx, network, address)
			if err == nil {
				p.socket = socket
				err = socket.SetDeadline(time.Now().Add(brokerTimeout))
			}
			return socket, err
		},
	})
	if err != nil {
		if p.socket != nil {
			p.socket.Close()
		}
		return nil, errors.New("broker connection failed") // Do not log credential-bearing URLs.
	}
	p.connection = connection
	// Channel setup RPCs also have a deadline. Closing the socket interrupts blocked I/O.
	setupCtx, cancel := context.WithTimeout(ctx, brokerTimeout)
	defer cancel()
	abort := context.AfterFunc(setupCtx, func() { p.socket.Close() })
	defer abort()
	p.channel, err = connection.Channel()
	if err == nil {
		err = p.channel.ExchangeDeclarePassive(exchange, "fanout", true, false, false, false, nil)
	}
	if err == nil {
		err = p.channel.Confirm(false)
	}
	if err != nil || setupCtx.Err() != nil {
		p.close()
		return nil, errors.New("broker exchange/confirm setup failed")
	}
	p.returns = p.channel.NotifyReturn(make(chan amqp.Return, 1))
	return p, nil
}

func (p *publisher) close() { p.socket.Close() }

func (p *publisher) publish(ctx context.Context, message store.OutboxMessage) error {
	ctx, cancel := context.WithTimeout(ctx, brokerTimeout)
	defer cancel()
	// The library context only checks cancellation before writing, so interrupt socket I/O too.
	abort := context.AfterFunc(ctx, func() { p.socket.Close() })
	defer abort()
	confirmation, err := p.channel.PublishWithDeferredConfirmWithContext(ctx, p.exchange, "", true, false, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent,
		MessageId: strconv.FormatInt(message.MessageID, 10), CorrelationId: message.CorrelationID,
		Timestamp: message.CreatedAt, Type: "HazardEvent", Body: message.Payload,
		Headers: amqp.Table{"X-Correlation-ID": message.CorrelationID, "schema_version": int32(1), "hazard_revision": message.Revision},
	})
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	ack, err := confirmation.WaitContext(ctx)
	if err != nil || !ack {
		return errors.New("publisher confirmation missing or negative")
	}
	// One in-flight message: the library dispatches basic.return before the following confirm.
	select {
	case returned, ok := <-p.returns:
		if !ok {
			return errors.New("broker channel closed")
		}
		return fmt.Errorf("mandatory publish returned: %d %s", returned.ReplyCode, returned.ReplyText)
	default:
		return nil
	}
}

// Run uses one worker per Aggregator instance. ponytail: M1 runs one replica;
// add row leases before deploying multiple publishers to avoid unnecessary duplicates.
func Run(ctx context.Context, repository *store.PostgresRepository, brokerURL, exchange string, logger *slog.Logger) {
	var p *publisher
	defer func() {
		if p != nil {
			p.close()
		}
	}()
	for ctx.Err() == nil {
		if p == nil {
			var err error
			p, err = connect(ctx, brokerURL, exchange)
			if err != nil {
				logger.Warn("outbox waiting for broker", "service", "aggregator", "error", err)
			} else {
				logger.Info("outbox publisher connected", "service", "aggregator", "exchange", p.exchange)
			}
		}
		if p != nil {
			messages, err := repository.PendingOutbox(ctx)
			if err != nil {
				logger.Warn("outbox read failed", "error", err)
			} else {
				logger.Info("outbox batch", "pending_batch", len(messages))
				for _, message := range messages {
					started := time.Now()
					err := p.publish(ctx, message)
					if err == nil {
						err = repository.MarkPublished(ctx, message.MessageID)
					}
					attrs := []any{"service", "aggregator", "target", "message-broker", "message_id", message.MessageID,
						"hazard_id", message.HazardID, "hazard_revision", message.Revision, "correlation_id", message.CorrelationID,
						"latency_ms", float64(time.Since(started).Microseconds()) / 1000}
					if err != nil {
						logger.Warn("outbox publish pending", append(attrs, "error", err)...)
						p.close()
						p = nil
						break
					}
					logger.Info("outbox publish confirmed", attrs...)
				}
				if len(messages) == 50 && p != nil {
					continue // Drain a backlog without sleeping between full batches.
				}
			}
		}
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
