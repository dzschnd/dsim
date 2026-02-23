package main

import (
	"log"
	"net/http"

	"github.com/dzschnd/dsim/internal/routes"
	"github.com/joho/godotenv"
)

func (app *application) LoadEnv() error {
	return godotenv.Load()
}

func (app *application) mount() http.Handler {
	return routes.NewRouter()
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
	// logger
	// db driver
}

type config struct {
	addr string
	// db dbConfig
}
