package main

import (
	"fmt"
	"github.com/lmittmann/tint"
	"log/slog"
	"os"
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
	}
	if err := api.initDocker(); err != nil {
		slog.Error("Failed to init docker client", "err", err)
	}
	defer api.closeDocker()
	api.initStore()
	if err := api.run(api.mount()); err != nil {
		slog.Error("Failed to start server", "port", port, "err", err)
	}
}
