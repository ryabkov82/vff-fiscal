package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/config"
	"github.com/ryabkov82/vff-fiscal/internal/httpapi"
	"github.com/ryabkov82/vff-fiscal/internal/lknpd"
	"github.com/ryabkov82/vff-fiscal/internal/state"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	store, err := state.Open(cfg.StatePath, state.AuthState{
		RefreshToken: cfg.InitialRefreshToken,
		DeviceID:     cfg.InitialDeviceID,
		INN:          cfg.InitialINN,
	})
	if err != nil {
		logger.Error("open state store", "error", err)
		os.Exit(1)
	}

	client := lknpd.New(lknpd.Config{
		BaseURL:        cfg.BaseURL,
		UserAgent:      cfg.UserAgent,
		TimezoneOffset: cfg.TimezoneOffset,
		PaymentType:    cfg.PaymentType,
		Timeout:        cfg.HTTPTimeout,
	}, store)

	api := httpapi.New(cfg.APIKey, cfg.DefaultServiceName, client, store, logger)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("vff-fiscal started", "listen_addr", cfg.ListenAddr, "base_url", cfg.BaseURL)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
