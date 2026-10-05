package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const serviceName = "dashboard-consumer"
const queueName = "dashboard"
const journalPath = "/data/events.jsonl"
const brokerTimeout = 5 * time.Second

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		response, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://127.0.0.1:8080/health")
		if err != nil {
			os.Exit(1)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", serviceName)
	brokerURL := strings.TrimSpace(os.Getenv("BROKER_URL"))
	if _, err := amqp.ParseURI(brokerURL); err != nil || brokerURL == "" {
		logger.Error("valid BROKER_URL is required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var healthy atomic.Bool
	server := &http.Server{Addr: ":8080", ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			status := "ready"
			if !healthy.Load() {
				status = "unavailable"
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			fmt.Fprintf(w, `{"service":%q,"status":%q}`, serviceName, status)
		})}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server failed", "error", err)
			stop()
		}
	}()
	run(ctx, brokerURL, journalPath, &healthy, logger)
	healthy.Store(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)
}

func run(ctx context.Context, brokerURL, path string, healthy *atomic.Bool, logger *slog.Logger) {
	for ctx.Err() == nil {
		err := session(ctx, brokerURL, path, healthy, logger)
		healthy.Store(false)
		if ctx.Err() != nil {
			return
		}
		logger.Warn("consumer retry pending", "error", err)
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func session(ctx context.Context, brokerURL, path string, healthy *atomic.Bool, logger *slog.Logger) error {
	healthy.Store(false)
	j, err := openJournal(path)
	if err != nil {
		return fmt.Errorf("journal unavailable: %w", err)
	}
	defer j.file.Close()
	logger.Info("journal restored", "processed_ids", len(j.seen), "latest_hazards", len(j.latest))
	var socket net.Conn
	connection, err := amqp.DialConfig(brokerURL, amqp.Config{Heartbeat: 5 * time.Second,
		Dial: func(network, address string) (net.Conn, error) {
			var err error
			socket, err = (&net.Dialer{Timeout: brokerTimeout}).DialContext(ctx, network, address)
			if err == nil {
				err = socket.SetDeadline(time.Now().Add(brokerTimeout))
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
	setupCtx, cancel := context.WithTimeout(ctx, brokerTimeout)
	setupAbort := context.AfterFunc(setupCtx, func() { socket.Close() })
	defer cancel()
	defer setupAbort()
	channel, err := connection.Channel()
	if err != nil {
		return errors.New("broker channel setup failed")
	}
	if err := channel.ExchangeDeclarePassive("hazard.events", "fanout", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := channel.QueueDeclare(queueName, true, false, false, false, amqp.Table{"x-queue-type": "classic"}); err != nil {
		return err
	}
	if err := channel.QueueBind(queueName, "", "hazard.events", false, nil); err != nil {
		return err
	}
	if err := channel.Qos(1, 0, false); err != nil {
		return err
	}
	deliveries, err := channel.Consume(queueName, serviceName, false, true, false, false, nil)
	if err != nil {
		return err
	}
	if !setupAbort() || setupCtx.Err() != nil {
		return errors.New("broker subscription setup timed out")
	}
	cancel()
	healthy.Store(true)
	defer healthy.Store(false)
	logger.Info("consumer subscribed", "queue", queueName, "prefetch", 1, "manual_ack", true)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("broker delivery stream closed")
			}
			// Ack/reject writes are bounded even when the broker stops reading.
			if err := socket.SetWriteDeadline(time.Now().Add(brokerTimeout)); err != nil {
				return err
			}
			if err := handle(delivery, j, logger); err != nil {
				return err
			}
			if err := socket.SetWriteDeadline(time.Time{}); err != nil {
				return err
			}
		}
	}
}

func handle(delivery amqp.Delivery, j *journal, logger *slog.Logger) error {
	started := time.Now()
	r, err := decode(delivery)
	if err != nil {
		logger.Error("invalid event rejected", "message_id", delivery.MessageId, "correlation_id", delivery.CorrelationId, "error", err, "requeue", false)
		return delivery.Reject(false) // No DLQ for M1; malformed payload is discarded for this subscription.
	}
	result, err := j.save(r)
	if err != nil {
		logger.Error("journal write failed; event unacknowledged", "message_id", r.MessageID, "correlation_id", r.CorrelationID, "error", err)
		return err
	}
	if err := delivery.Ack(false); err != nil {
		return err
	}
	logger.Info("event handled", "message_id", r.MessageID, "hazard_id", r.HazardID, "hazard_revision", r.Revision,
		"correlation_id", r.CorrelationID, "result", result, "redelivered", delivery.Redelivered,
		"latency_ms", float64(time.Since(started).Microseconds())/1000)
	return nil
}
