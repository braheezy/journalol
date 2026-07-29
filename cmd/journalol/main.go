package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"journalol/internal/config"
	"journalol/internal/model"
	"journalol/internal/store"
	journalweb "journalol/internal/web"
)

var errPrimaryPlayerRequired = errors.New(
	`no primary player is configured; set JOURNALOL_DEMO=true or run "journalol seed-demo" before starting the server`,
)

type demoSeeder interface {
	SeedDemo(context.Context) error
}

type primaryPlayerReader interface {
	PrimaryPlayer(context.Context) (*model.PlayerProfile, error)
}

type demoSeedState uint8

const (
	demoSeedNotRequested demoSeedState = iota
	demoSeedReady
	demoSeedSkippedConflict
)

func main() {
	os.Exit(run())
}

func run() int {
	return runWithIO(os.Args, os.Stdout, os.Stderr)
}

func runWithIO(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	command := "serve"
	if len(args) > 1 {
		command = args[1]
	}

	switch command {
	case "healthcheck":
		if err := healthcheck(cfg.Addr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "serve", "seed-demo":
	default:
		fmt.Fprintf(stderr, "unknown command %q (expected serve, seed-demo, or healthcheck)\n", command)
		return 2
	}

	logger := slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	dataStore, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Error("open database", "error", err)
		return 1
	}
	defer func() {
		if err := dataStore.Close(); err != nil {
			logger.Error("close database", "error", err)
		}
	}()

	seedState, err := prepareDemoData(
		context.Background(),
		command,
		cfg.Demo,
		dataStore,
	)
	if err != nil {
		logger.Error("seed demo data", "error", err)
		return 1
	}
	if seedState == demoSeedSkippedConflict {
		logger.Warn("automatic demo seed skipped because a non-demo profile exists")
	}
	if command == "seed-demo" {
		logger.Info("demo data is ready")
		return 0
	}

	if err := requirePrimaryPlayer(context.Background(), dataStore); err != nil {
		logger.Error("cannot start server", "error", err)
		return 1
	}

	app, err := journalweb.NewServer(dataStore, cfg.Location, cfg.AllowedHosts, logger)
	if err != nil {
		logger.Error("initialize web server", "error", err)
		return 1
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	rootContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("journalol is ready", "address", cfg.Addr, "database", cfg.DBPath, "demo", cfg.Demo)
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve HTTP", "error", err)
			return 1
		}
	case <-rootContext.Done():
		logger.Info("shutting down")
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			logger.Error("graceful shutdown", "error", err)
			return 1
		}
	}

	return 0
}

func prepareDemoData(
	ctx context.Context,
	command string,
	demoEnabled bool,
	dataStore demoSeeder,
) (demoSeedState, error) {
	if command != "seed-demo" && !demoEnabled {
		return demoSeedNotRequested, nil
	}

	err := dataStore.SeedDemo(ctx)
	if err == nil {
		return demoSeedReady, nil
	}
	if command == "serve" && errors.Is(err, store.ErrDemoProfileConflict) {
		return demoSeedSkippedConflict, nil
	}
	return demoSeedNotRequested, err
}

func requirePrimaryPlayer(ctx context.Context, dataStore primaryPlayerReader) error {
	_, err := dataStore.PrimaryPlayer(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return errPrimaryPlayerRequired
	default:
		return fmt.Errorf("load primary player: %w", err)
	}
}

func healthcheck(listenAddress string) error {
	url, err := healthURL(listenAddress)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("health request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	return nil
}

func healthURL(listenAddress string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return "", fmt.Errorf("parse JOURNALOL_ADDR: %w", err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/readyz", nil
}
