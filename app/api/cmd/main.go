package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lmittmann/tint"
)

func main() {

	logger := slog.New(tint.NewHandler(os.Stdout,
		&tint.Options{
			Level:      slog.LevelInfo,
			TimeFormat: "15:04:05.000",
			NoColor:    false,
		},
	))
	slog.SetDefault(logger)

	api := application{}

	if err := api.LoadEnv(); err != nil {
		slog.Error("Failed to load env", "err", err)
		os.Exit(1)
	}
	if err := api.LoadConfig(); err != nil {
		slog.Error("Failed to load config", "err", err)
		os.Exit(1)
	}
	if err := api.initDocker(); err != nil {
		slog.Error("Failed to init docker client", "err", err)
		os.Exit(1)
	}
	if err := api.ensureNodeImage(context.Background()); err != nil {
		slog.Error("Failed to ensure node image", "err", err)
		os.Exit(1)
	}
	if err := api.initStore(); err != nil {
		slog.Error("Failed to init store", "err", err)
		os.Exit(1)
	}

	srv := api.newServer(api.mount())

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		slog.Error("Server failed to start", "addr", srv.Addr,
			"err", err)
		os.Exit(1)
	}

	slog.Info("Server started", "url", fmt.Sprintf("http://localhost%s", srv.Addr))

	go func() {
		if err := srv.Serve(ln); err != nil && err !=
			http.ErrServerClosed {
			slog.Error("Server stopped unexpectedly", "err", err)
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

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cleanupCancel()
	api.cleanUp(cleanupCtx)
}
