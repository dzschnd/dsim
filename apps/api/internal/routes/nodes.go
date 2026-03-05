package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type createNodeResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
}

func (s *Server) createNodeHandler(w http.ResponseWriter, r *http.Request) {
	// TODO: Return a message to user
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	image := nodeImageTag()

	if _, _, err := s.docker.ImageInspectWithRaw(ctx, image); err != nil {
		if client.IsErrNotFound(err) {
			writeJSONError(w, http.StatusNotFound, "image not found: "+image)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "image inspect failed")
		return
	}

	createResp, err := s.docker.ContainerCreate(ctx, &container.Config{Image: image}, nil, nil, nil, "")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "container create failed")
		return
	}

	inspect, err := s.docker.ContainerInspect(ctx, createResp.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "container inspect failed")
		return
	}

	name := strings.TrimPrefix(inspect.Name, "/")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createNodeResponse{
		ID:    createResp.ID,
		Name:  name,
		Image: image,
	})
}

func nodeImageTag() string {
	image := strings.TrimSpace(os.Getenv("NODE_IMAGE"))
	if image == "" {
		return "dsim/host:local"
	}
	return image
}
