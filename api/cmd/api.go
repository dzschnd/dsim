package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/routes"
	"github.com/dzschnd/dsim/internal/store"
	"github.com/dzschnd/dsim/internal/topology"
	"github.com/joho/godotenv"
)

func (app *application) LoadEnv() error {
	if err := godotenv.Load(); err != nil {
		if os.IsNotExist(err) {
			slog.Info(".env not found, using process environment")
			return nil
		}
		return err
	}
	slog.Info("Env loaded")
	return nil
}

func (app *application) LoadConfig() error {
	portEnv := strings.TrimSpace(os.Getenv("PORT"))
	if portEnv == "" {
		return fmt.Errorf("PORT not set")
	}
	port, err := strconv.Atoi(portEnv)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid PORT: %s", portEnv)
	}
	app.config = config{
		addr: fmt.Sprintf(":%d", port),
	}
	return nil
}

func (app *application) mount() http.Handler {
	apiHandler := routes.NewRouter(routes.NewServer(app.docker, app.store))

	clientDistDir := strings.TrimSpace(os.Getenv("CLIENT_DIST_DIR"))
	if clientDistDir == "" {
		return apiHandler
	}

	clientHandler := http.FileServer(http.Dir(clientDistDir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" {
			http.ServeFile(w, r, fmt.Sprintf("%s/index.html", clientDistDir))
			return
		}
		clientHandler.ServeHTTP(w, r)
	})
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

func (app *application) ensureNodeImage(ctx context.Context) error {
	image := strings.TrimSpace(os.Getenv("NODE_IMAGE"))
	if image == "" {
		return fmt.Errorf("NODE_IMAGE not set")
	}
	contextDir := strings.TrimSpace(os.Getenv("NODE_IMAGE_PATH"))
	if contextDir == "" {
		return fmt.Errorf("NODE_IMAGE_PATH not set")
	}

	if _, _, err := app.docker.ImageInspectWithRaw(ctx, image); err == nil {
		slog.Info("Node image exists", "image", image)
		return nil
	} else if !client.IsErrNotFound(err) {
		return fmt.Errorf("node image inspect failed: %w", err)
	}

	if info, err := os.Stat(contextDir); err != nil || !info.IsDir() {
		if err != nil {
			return fmt.Errorf("node image build context not found at %s: %w", contextDir, err)
		}
		return fmt.Errorf("node image build context path is not a directory: %s", contextDir)
	}

	slog.Info("Node image not found, building", "image", image, "context", contextDir)

	var buildContext bytes.Buffer
	tw := tar.NewWriter(&buildContext)
	if err := filepath.Walk(contextDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(contextDir, path)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(tw, file)
		return err
	}); err != nil {
		_ = tw.Close()
		return fmt.Errorf("failed to prepare node image build context: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("failed to finalize node image build context: %w", err)
	}

	resp, err := app.docker.ImageBuild(ctx, &buildContext, types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       []string{image},
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("node image build request failed: %w", err)
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	for {
		var message struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := decoder.Decode(&message); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to read node image build output: %w", err)
		}

		if strings.TrimSpace(message.Stream) != "" {
			for line := range strings.SplitSeq(strings.TrimSuffix(message.Stream, "\n"), "\n") {
				if strings.TrimSpace(line) != "" {
					slog.Info("Node image build", "message", line)
				}
			}
		}
		if message.Error != "" {
			return fmt.Errorf("node image build failed: %s", message.Error)
		}
	}

	slog.Info("Node image build complete", "image", image)
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
	topology.CleanupRuntime(ctx, app.docker, app.store)
	if err := topology.ClearStore(ctx, app.docker, app.store); err != nil {
		slog.Error("App cleanup store reset failed", "err", err)
	}

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
