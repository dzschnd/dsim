package main

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/docker/docker/api/types/container"
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

func (app *application) cleanUp(ctx context.Context) {
	if app.docker == nil {
		return
	}
	if app.store == nil {
		_ = app.docker.Close()
		slog.Info("Docker closed")
		return
	}

	slog.Info("Cleaning up docker resources")

	nodes := app.store.NodesSnapshot()
	links := app.store.LinksSnapshot()

	const linkPoolSize = 6
	const nodePoolSize = 2 * linkPoolSize
	linkSem := make(chan struct{}, linkPoolSize)
	nodeSem := make(chan struct{}, nodePoolSize)
	var wg sync.WaitGroup

	for _, node := range nodes {
		if node.ContainerID == "" && node.NetworkID == "" {
			continue
		}
		node := node
		wg.Go(func() {
			nodeSem <- struct{}{}
			defer func() { <-nodeSem }()
			if node.ContainerID != "" {
				if err := app.docker.ContainerRemove(ctx, node.ContainerID, container.RemoveOptions{Force: true}); err != nil && !client.IsErrNotFound(err) {
					slog.Error("Container remove failed", "container_id", node.ContainerID, "err", err)
				} else {
					slog.Info("Container removed", "container_id", node.ContainerID)
				}
			}

			if node.NetworkID != "" {
				if err := app.docker.NetworkRemove(ctx, node.NetworkID); err != nil && !client.IsErrNotFound(err) {
					slog.Error("Isolated network remove failed", "network_id", node.NetworkID, "err", err)
				} else {
					slog.Info("Isolated network removed", "network_id", node.NetworkID)
				}
			}
		})
	}
	wg.Wait()

	for _, link := range links {
		if link.NetworkID == "" {
			continue
		}
		link := link
		wg.Go(func() {
			linkSem <- struct{}{}
			defer func() { <-linkSem }()
			if err := app.docker.NetworkRemove(ctx, link.NetworkID); err != nil && !client.IsErrNotFound(err) {
				slog.Error("Link network remove failed", "network_id", link.NetworkID, "err", err)
			} else {
				slog.Info("Link network removed", "network_id", link.NetworkID)
			}
		})
	}
	wg.Wait()

	_ = app.docker.Close()
	slog.Info("Docker closed")
}

func (app *application) initStore() error {
	if app.store == nil {
		newStore, err := store.NewStore(context.Background(), app.docker)
		if err != nil {
			return err
		}
		app.store = newStore
		slog.Info("Store initialized")
	}
	return nil
}
