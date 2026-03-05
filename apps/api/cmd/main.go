package main

import (
	"fmt"
	"log"
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

	if err := api.LoadEnv(); err != nil {
		log.Fatalf("Failed to load env. Error: %s", err)
	}
	if err := api.initDocker(); err != nil {
		log.Fatalf("Failed to init docker client. Error: %s", err)
	}
	defer api.closeDocker()
	api.initStore()
	if err := api.run(api.mount()); err != nil {
		log.Fatalf("Failed to start server. Error: %s", err)
	}
}
