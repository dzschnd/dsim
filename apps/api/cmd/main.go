package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lmittmann/tint"
)

func main() {
	port := 8080

	addr := fmt.Sprintf(":%d", port)
	cfg := config{
		addr,
	}

	api := application{
		config: cfg,
	}

	logger := slog.New(tint.NewHandler(os.Stdout,
		&tint.Options{
			Level:      slog.LevelInfo,
			TimeFormat: "15:04:05",
			NoColor:    false,
		},
	))
	slog.SetDefault(logger)

	if err := api.LoadEnv(); err != nil {
		slog.Error("Failed to load env", "err", err)
		os.Exit(1)
	}
	if err := api.initDocker(); err != nil {
		slog.Error("Failed to init docker client", "err", err)
		os.Exit(1)
	}
	if err := api.initStore(); err != nil {
		slog.Error("Failed to init store", "err", err)
		os.Exit(1)
	}

	srv := api.newServer(api.mount())

	slog.Info("Starting server", "url", fmt.Sprintf("http://localhost%s", srv.Addr))
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server stopped", "err", err)
		}
	}()

	shutdown, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-shutdown.Done()

	slog.Info("Shutting down server")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Shutdown with error", "err", err)
	} else {
		slog.Info("Shutdown complete")
	}

	api.cleanUp()
}
