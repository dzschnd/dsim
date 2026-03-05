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
	ID          string `json:"id"`
	Name        string `json:"name"`
	Image       string `json:"image"`
	ContainerID string `json:"containerId"`
}

func (s *Server) createNodeHandler(w http.ResponseWriter, r *http.Request) {
	// TODO: Return a message to user
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if s.store == nil {
		writeJSONError(w, http.StatusInternalServerError, "store not initialized")
		return
	}

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
	nodeID := newID("node_")
	node := Node{
		ID:          nodeID,
		Name:        name,
		Image:       image,
		ContainerID: createResp.ID,
		CreatedAt:   time.Now().UTC(),
	}
	s.store.AddNode(node)

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createNodeResponse{
		ID:          node.ID,
		Name:        node.Name,
		Image:       node.Image,
		ContainerID: node.ContainerID,
	})
}

func nodeImageTag() string {
	image := strings.TrimSpace(os.Getenv("NODE_IMAGE"))
	if image == "" {
		return "dsim/host:local"
	}
	return image
}

func (s *Server) listNodesHandler(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONError(w, http.StatusInternalServerError, "store not initialized")
		return
	}
	nodes := s.store.ListNodes()
	_ = json.NewEncoder(w).Encode(nodes)
}

func (s *Server) deleteNodeHandler(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONError(w, http.StatusInternalServerError, "store not initialized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	nodeID := strings.TrimSpace(r.PathValue("id"))
	if nodeID == "" {
		writeJSONError(w, http.StatusBadRequest, "node id required")
		return
	}

	node, ok := s.store.GetNode(nodeID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "node not found")
		return
	}

	links := s.store.ListLinks()
	for _, link := range links {
		if link.NodeAID == nodeID || link.NodeBID == nodeID {
			s.removeLinkNetwork(ctx, link)
			s.store.DeleteLink(link.ID)
		}
	}

	if node.ContainerID != "" {
		err := s.docker.ContainerRemove(ctx, node.ContainerID, container.RemoveOptions{Force: true})
		if err != nil && !client.IsErrNotFound(err) {
			writeJSONError(w, http.StatusInternalServerError, "container remove failed")
			return
		}
	}

	s.store.DeleteNode(nodeID)
	w.WriteHeader(http.StatusNoContent)
}
