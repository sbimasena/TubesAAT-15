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

	"github.com/sbimasena/TubesAAT-15/services/auth/internal/config"
	"github.com/sbimasena/TubesAAT-15/services/auth/internal/httpapi"
	"github.com/sbimasena/TubesAAT-15/services/auth/internal/identity"
	"github.com/sbimasena/TubesAAT-15/services/auth/internal/session"
	"github.com/sbimasena/TubesAAT-15/services/auth/internal/token"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		if err := checkHealth("http://127.0.0.1:" + os.Getenv("AUTH_PORT") + "/health"); err != nil {
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("invalid configuration", "service", "auth", "error", err)
		os.Exit(1)
	}

	identities, err := identity.NewFromEnv(os.Getenv)
	if err != nil {
		logger.Error("invalid identities", "service", "auth", "error", err)
		os.Exit(1)
	}
	issuer, err := token.New([]byte(os.Getenv("JWT_SIGNING_SECRET")), cfg.AccessTokenTTL)
	if err != nil {
		logger.Error("invalid token configuration", "service", "auth", "error", err)
		os.Exit(1)
	}
	sessions := session.New(identities, issuer, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.NewHandler(logger, sessions, os.Getenv("AUTH_INTERNAL_SECRET")),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
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
			logger.Error("server shutdown failed", "service", "auth", "error", err)
		}
	}()

	logger.Info("service starting", "service", "auth", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "service", "auth", "error", err)
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
