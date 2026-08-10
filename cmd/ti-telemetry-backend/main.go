package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/tidbcloud/ti-cli/internal/telemetrybackend"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	config, err := telemetrybackend.LoadConfig(os.Getenv)
	if err != nil {
		logger.Error("telemetry backend configuration is invalid", "error", err.Error())
		os.Exit(1)
	}

	db, err := sql.Open("mysql", config.TiDBDSN)
	if err != nil {
		logger.Error("open TiDB connection failed")
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	tidbSink := telemetrybackend.NewTiDBSink(db)

	postHogSink, err := telemetrybackend.NewPostHogSink(
		config.PostHogAPIHost,
		config.PostHogProjectToken,
		config.Environment,
		&http.Client{},
	)
	if err != nil {
		logger.Error("initialize PostHog sink failed")
		os.Exit(1)
	}

	metrics := &telemetrybackend.Metrics{}
	batcher := telemetrybackend.NewBatcher(
		config,
		[]telemetrybackend.Sink{tidbSink, postHogSink},
		logger,
		metrics,
	)
	batcher.Start()
	api := telemetrybackend.NewServer(config, batcher, tidbSink, postHogSink, logger, metrics)
	httpServer := &http.Server{
		Addr:              config.BindAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("telemetry backend listening", "bind_addr", config.BindAddr)
		serverErrors <- httpServer.ListenAndServe()
	}()

	serverFailed := false
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("telemetry HTTP server failed", "error", err.Error())
			serverFailed = true
		}
	}

	httpShutdownContext, httpShutdownCancel := context.WithTimeout(
		context.Background(),
		config.ShutdownDrainTimeout,
	)
	if err := httpServer.Shutdown(httpShutdownContext); err != nil {
		logger.Error("telemetry HTTP shutdown failed")
	}
	httpShutdownCancel()

	batchShutdownContext, batchShutdownCancel := context.WithTimeout(
		context.Background(),
		config.ShutdownDrainTimeout,
	)
	defer batchShutdownCancel()
	if err := batcher.Close(batchShutdownContext); err != nil {
		logger.Error("telemetry batch drain failed")
	}
	if serverFailed {
		_ = db.Close()
		os.Exit(1)
	}
}
