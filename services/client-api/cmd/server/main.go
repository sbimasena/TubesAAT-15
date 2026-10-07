package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/authclient"
	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/config"
	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/httpapi"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		port := os.Getenv("CLIENT_API_PORT")
		if port == "" {
			port = "8080"
		}
		if err := checkHealth("http://127.0.0.1:" + port + "/health"); err != nil {
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("invalid configuration", "service", "client-api", "error", err)
		os.Exit(1)
	}
	aggregatorClient, err := aggregator.NewClient(cfg.AggregatorBaseURL, cfg.AggregatorRequestTimeout, logger)
	if err != nil {
		logger.Error("invalid Aggregator configuration", "service", "client-api", "error", err)
		os.Exit(1)
	}

	authClient, err := authclient.New(cfg.AuthBaseURL, os.Getenv("AUTH_INTERNAL_SECRET"), cfg.AuthRequestTimeout, logger)
	if err != nil {
		logger.Error("invalid Auth configuration", "service", "client-api", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.NewHandler(aggregatorClient, authClient, cfg.EnableProvisionalHazards, cfg.MaxConcurrentRequests, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown failed", "service", "client-api", "error", err)
		}
	}()

	logger.Info("service starting", "service", "client-api", "addr", server.Addr,
		"provisional_hazards_enabled", cfg.EnableProvisionalHazards, "max_concurrent_requests", cfg.MaxConcurrentRequests)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "service", "client-api", "error", err)
		os.Exit(1)
	}
	<-shutdownDone
}

func checkHealth(endpoint string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}
