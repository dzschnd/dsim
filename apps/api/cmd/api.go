package main

import (
	"log"
	"net/http"

	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/routes"
	"github.com/joho/godotenv"
)

func (app *application) LoadEnv() error {
	return godotenv.Load()
}

func (app *application) mount() http.Handler {
	return routes.NewRouter(routes.NewServer(app.docker, app.store))
}

func (app *application) run(h http.Handler) error {
	srv := &http.Server{
		Addr:    app.config.addr,
		Handler: h,
	}

	log.Printf("Server listening at http://localhost%s\n", app.config.addr)
	return srv.ListenAndServe()
}

type application struct {
	config config
	docker *client.Client
	store  *routes.Store
	// logger
	// db driver
}

type config struct {
	addr string
	// db dbConfig
}

func (app *application) initDocker() error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	app.docker = cli
	return nil
}

func (app *application) closeDocker() {
	if app.docker == nil {
		return
	}
	_ = app.docker.Close()
}

func (app *application) initStore() {
	if app.store == nil {
		app.store = routes.NewStore()
	}
}
