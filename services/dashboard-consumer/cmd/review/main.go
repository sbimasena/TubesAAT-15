// Temporary P5 subscriber. Uses its own queue/connection; no canonical storage access.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "review-subscriber")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := subscribe(ctx, os.Getenv("BROKER_URL"), logger); err != nil && ctx.Err() == nil {
		logger.Error("review subscriber failed", "error", err)
		os.Exit(1)
	}
}

func subscribe(ctx context.Context, brokerURL string, logger *slog.Logger) error {
	if brokerURL == "" {
		return errors.New("BROKER_URL is required")
	}
	var socket net.Conn
	connection, err := amqp.DialConfig(brokerURL, amqp.Config{Heartbeat: 5 * time.Second,
		Dial: func(network, address string) (net.Conn, error) {
			var err error
			socket, err = (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
			if err == nil {
				err = socket.SetDeadline(time.Now().Add(5 * time.Second))
			}
			return socket, err
		}})
	if socket != nil {
		defer socket.Close()
	}
	if err != nil {
		return errors.New("broker connection failed")
	}
	abort := context.AfterFunc(ctx, func() { socket.Close() })
	defer abort()
	setupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	setupAbort := context.AfterFunc(setupCtx, func() { socket.Close() })
	defer cancel()
	defer setupAbort()
	channel, err := connection.Channel()
	if err != nil {
		return err
	}
	if err := channel.ExchangeDeclarePassive("hazard.events", "fanout", true, false, false, false, nil); err != nil {
		return err
	}
	queue, err := channel.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		return err
	}
	if err := channel.QueueBind(queue.Name, "", "hazard.events", false, nil); err != nil {
		return err
	}
	if err := channel.Qos(1, 0, false); err != nil {
		return err
	}
	deliveries, err := channel.Consume(queue.Name, "review", false, true, false, false, nil)
	if err != nil {
		return err
	}
	if !setupAbort() || setupCtx.Err() != nil {
		return errors.New("review subscription setup timed out")
	}
	cancel()
	logger.Info("review subscribed", "queue", queue.Name, "exchange", "hazard.events", "durable", false, "exclusive", true)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("review delivery stream closed")
			}
			var payload map[string]any
			if err := json.Unmarshal(delivery.Body, &payload); err != nil {
				return err
			}
			logger.Info("review event received", "message_id", delivery.MessageId, "hazard_id", payload["hazard_id"],
				"correlation_id", delivery.CorrelationId, "headers", delivery.Headers, "payload", payload)
			if err := socket.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return err
			}
			if err := delivery.Ack(false); err != nil {
				return err
			}
			if err := socket.SetWriteDeadline(time.Time{}); err != nil {
				return err
			}
		}
	}
}
