package main

import (
	"log/slog"
	"net/http"

	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/routes"
	"github.com/dzschnd/dsim/internal/store"
	"github.com/joho/godotenv"
)

func (app *application) LoadEnv() error {
	if err := godotenv.Load(); err != nil {
		return err
	}
	slog.Info("Env loaded")
	return nil
}

func (app *application) mount() http.Handler {
	return routes.NewRouter(routes.NewServer(app.docker, app.store))
}

func (app *application) newServer(h http.Handler) *http.Server {
	srv := &http.Server{
		Addr:    app.config.addr,
		Handler: h,
	}

	return srv
}

type application struct {
	config config
	docker *client.Client
	store  *store.Store
}

type config struct {
	addr string
}

func (app *application) initDocker() error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	app.docker = cli
	slog.Info("Docker initialized")
	return nil
}

func (app *application) cleanUp() {
	if app.docker == nil {
		return
	}
	_ = app.docker.Close()
	slog.Info("Docker closed")
}

func (app *application) initStore() {
	if app.store == nil {
		app.store = store.NewStore()
		slog.Info("Store initialized")
	}
}
