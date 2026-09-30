package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/httpapi"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/ingest"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/source"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	port := envOr("AGGREGATOR_PORT", "8083")
	interval := positiveSecondsFromEnv("POLL_INTERVAL_SECONDS", 3)
	bmkgKey := requiredEnv(logger, "BMKG_API_KEY")
	pvmbgToken := requiredEnv(logger, "PVMBG_TOKEN")
	bmkgBaseURL := requiredEnv(logger, "BMKG_BASE_URL")
	pvmbgBaseURL := requiredEnv(logger, "PVMBG_BASE_URL")
	if bmkgKey == pvmbgToken {
		logger.Error("upstream credentials must be different", "variables", "BMKG_API_KEY,PVMBG_TOKEN")
		os.Exit(1)
	}
	repository := store.NewMemoryRepository(10000)
	publisher := ingest.NewLogPublisher(logger)
	bmkg := source.NewBMKGClient(bmkgBaseURL, bmkgKey, logger)
	pvmbg := source.NewPVMBGClient(pvmbgBaseURL, pvmbgToken, logger)
	manager := ingest.NewManager(bmkg, pvmbg, repository, publisher, interval, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go manager.Run(ctx)

	api := httpapi.NewServer(repository, manager, logger)
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
