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
	"github.com/dzschnd/dsim/internal/model"
)

type createNodeRequest struct {
	Type string `json:"type"`
}
type createNodeResponse struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Type        model.NodeType `json:"type"`
	ContainerID string         `json:"containerId"`
}

func (s *Server) createNodeHandler(w http.ResponseWriter, r *http.Request) {
	// TODO: Return a message to user
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if s.store == nil {
		writeJSONError(w, http.StatusInternalServerError, "store not initialized")
		return
	}

	var req createNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "type not provided")
		return
	}
	nodeType, ok := model.NameNodeType[req.Type]
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid type")
		return
	}
	image := nodeTypeTag(nodeType)

	if _, _, err := s.docker.ImageInspectWithRaw(ctx, image); err != nil {
		if client.IsErrNotFound(err) {
			writeJSONError(w, http.StatusNotFound, "image not found: "+image)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "image inspect failed")
		return
	}

	initEnabled := true
	createResp, err := s.docker.ContainerCreate(ctx, &container.Config{Image: image}, &container.HostConfig{Init: &initEnabled}, nil, nil, "")
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
		Status:      model.Idle,
		Type:        nodeType,
		ContainerID: createResp.ID,
		CreatedAt:   time.Now().UTC(),
	}
	s.store.AddNode(node)

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createNodeResponse{
		ID:          node.ID,
		Name:        node.Name,
		Type:        node.Type,
		ContainerID: node.ContainerID,
	})
}

func nodeTypeTag(t model.NodeType) string {
	var image string
	switch t {
	case model.Host:
		image = strings.TrimSpace(os.Getenv("HOST_IMAGE"))
	//TODO: Add other images
	default:
		image = strings.TrimSpace(os.Getenv("HOST_IMAGE"))
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

func (s *Server) startNodeHandler(w http.ResponseWriter, r *http.Request) {
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

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			writeJSONError(w, http.StatusNotFound, "container not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "container inspect failed")
		return
	}
	if inspect.State != nil && inspect.State.Running {
		s.store.UpdateNodeStatus(nodeID, model.Running)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.docker.ContainerStart(ctx, node.ContainerID, container.StartOptions{}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to start node")
		return
	}
	s.store.UpdateNodeStatus(nodeID, model.Running)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stopNodeHandler(w http.ResponseWriter, r *http.Request) {
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

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			writeJSONError(w, http.StatusNotFound, "container not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "container inspect failed")
		return
	}
	if inspect.State != nil && !inspect.State.Running {
		s.store.UpdateNodeStatus(nodeID, model.Idle)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.docker.ContainerStop(ctx, node.ContainerID, container.StopOptions{}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to stop node: "+err.Error())
		return
	}
	s.store.UpdateNodeStatus(nodeID, model.Idle)
	w.WriteHeader(http.StatusNoContent)
}
