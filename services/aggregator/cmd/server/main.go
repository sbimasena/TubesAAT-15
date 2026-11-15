package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/httpapi"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/ingest"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/messaging"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/telemetry"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--inspect" {
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: /service --inspect <path> <correlation-id>")
			os.Exit(1)
		}
		if err := inspect(os.Args[2], os.Args[3], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "Aggregator inspection failed:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		client := &http.Client{Timeout: 4 * time.Second}
		response, err := client.Get("http://127.0.0.1:" + envOr("AGGREGATOR_PORT", "8083") + "/health")
		if err != nil {
			os.Exit(1)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	port := envOr("AGGREGATOR_PORT", "8083")
	interval := positiveSecondsFromEnv("POLL_INTERVAL_SECONDS", 3)
	bmkgKey := requiredEnv(logger, "BMKG_API_KEY")
	pvmbgToken := requiredEnv(logger, "PVMBG_TOKEN")
	bmkgBaseURL := requiredEnv(logger, "BMKG_BASE_URL")
	pvmbgBaseURL := requiredEnv(logger, "PVMBG_BASE_URL")
	brokerURL := requiredEnv(logger, "BROKER_URL")
	auditToken := requiredEnv(logger, "AUDIT_TOKEN")
	if bmkgKey == pvmbgToken {
		logger.Error("upstream credentials must be different", "variables", "BMKG_API_KEY,PVMBG_TOKEN")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownTracing := telemetry.Setup(ctx, "aggregator", logger)
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(flushCtx)
	}()
	repository, err := store.OpenRouter(requiredEnv(logger, "DATABASE_URL"), logger)
	if err != nil {
		logger.Error("canonical store initialization failed", "error", err)
		os.Exit(1)
	}
	defer repository.Close()
	// Best effort: a dead unit must not keep the Aggregator down; Run keeps retrying it.
	startupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	repository.CheckUnits(startupCtx)
	// Without the durable map the placement is unknown; restart rather than guess.
	if err := repository.LoadMap(startupCtx); err != nil {
		logger.Error("partition map unavailable", "error", err)
		os.Exit(1)
	}
	cancel()
	registerStoreMetrics(repository)
	routerDone := make(chan struct{})
	go func() {
		defer close(routerDone)
		repository.Run(ctx)
	}()
	reconcilerDone := make(chan struct{})
	go func() {
		defer close(reconcilerDone)
		// Epoch 0 like ingest until leader election (P2) provides the lease epoch.
		repository.Reconcile(ctx, 0, 10*time.Second, positiveSecondsFromEnv("REREPLICATE_AFTER_SECONDS", 60))
	}()
	bmkg := source.NewBMKGClient(bmkgBaseURL, bmkgKey, logger)
	pvmbg := source.NewPVMBGClient(pvmbgBaseURL, pvmbgToken, logger)
	manager := ingest.NewManager(bmkg, pvmbg, repository, interval, logger)
	pollersDone := make(chan struct{})
	go func() {
		defer close(pollersDone)
		manager.Run(ctx)
	}()
	publisherDone := make(chan struct{})
	go func() {
		defer close(publisherDone)
		messaging.Run(ctx, repository, brokerURL, "hazard.events", logger)
	}()

	api := httpapi.NewServer(repository, manager, logger)
	api.EnableAudit(repository, auditToken)
	api.EnableAnalytics(repository)
	// GET /internal/v1/status: A adds "leader"/"satellite", B adds "deadletter" here.
	api.AddStatus("storage", func(context.Context) any { return repository.StorageStatus() })
	api.AddStatus("sources", func(context.Context) any { return manager.Status() })
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown failed", "error", err)
		}
	}()

	logger.Info("service starting", "service", "aggregator", "addr", server.Addr, "poll_interval_seconds", interval.Seconds())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
	stop()
	<-pollersDone
	<-publisherDone
	<-routerDone
	<-reconcilerDone
}

// registerStoreMetrics exposes P1 placement per unit for the Grafana Overview.
func registerStoreMetrics(r *store.Router) {
	for _, unit := range r.UnitStats() {
		name := unit.Name
		stat := func() store.UnitStat {
			for _, s := range r.UnitStats() {
				if s.Name == name {
					return s
				}
			}
			return store.UnitStat{}
		}
		labels := prometheus.Labels{"unit": name}
		promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "bnpb_store_partitions", ConstLabels: labels,
			Help: "Replica slots the partition map assigns to the unit (16 each when 3 units are balanced)."},
			func() float64 { return float64(stat().Partitions) })
		promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "bnpb_store_unit_up", ConstLabels: labels,
			Help: "1 when the unit answered a health check in the last 15 s."},
			func() float64 {
				if stat().Up {
					return 1
				}
				return 0
			})
	}
}

// inspect lets local Docker operators read the running API without publishing a host port.
func inspect(path, correlation string, output io.Writer) error {
	parsed, err := url.ParseRequestURI(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || (parsed.Path != "/health" && parsed.Path != "/hazards" && parsed.Path != "/internal/v1/hazards") {
		return errors.New("expected a local health or hazard path")
	}
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+envOr("AGGREGATOR_PORT", "8083")+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-Correlation-ID", correlation)
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if response.Header.Get("X-Correlation-ID") != correlation {
		return errors.New("correlation ID changed")
	}
	_, err = io.Copy(output, response.Body)
	return err
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func requiredEnv(logger *slog.Logger, key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		logger.Error("required environment variable is missing", "name", key)
		os.Exit(1)
	}
	return value
}

func positiveSecondsFromEnv(key string, fallback int) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return time.Duration(fallback) * time.Second
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return time.Duration(fallback) * time.Second
	}
	return time.Duration(seconds) * time.Second
}
