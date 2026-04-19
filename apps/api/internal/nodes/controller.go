package nodes

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

type Handler struct {
	service *Service
}

type createNodeRequest struct {
	Type     string         `json:"type"`
	Position model.Position `json:"position"`
}

type createNodeResponse struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Position    model.Position `json:"position"`
	Type        model.NodeType `json:"type"`
	ContainerID string         `json:"containerId"`
}

type updateNodePositionRequest struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type commandRequest struct {
	Command string `json:"command"`
}

type commandResponse struct {
	Command  string `json:"command"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

func NewHandler(docker *client.Client, store *store.Store) *Handler {
	return &Handler{service: NewService(docker, store)}
}

func (h *Handler) CreateNodeHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	var req createNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSONError(w, http.StatusBadRequest, "type not provided")
		return
	}

	node, err := h.service.CreateNode(ctx, req.Type, req.Position)
	if err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createNodeResponse{
		ID:          node.ID,
		Name:        node.Name,
		Position:    node.Position,
		Type:        node.Type,
		ContainerID: node.ContainerID,
	})
}

func (h *Handler) ListNodesHandler(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.service.getNodes()
	if err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	_ = json.NewEncoder(w).Encode(nodes)
}

func (h *Handler) DeleteNodeHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	nodeID := strings.TrimSpace(r.PathValue("id"))
	if err := h.service.deleteNode(ctx, nodeID); err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) UpdateNodePositionHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	nodeID := strings.TrimSpace(r.PathValue("id"))

	var req updateNodePositionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSONError(w, http.StatusBadRequest, "invalid position request")
		return
	}

	if err := h.service.UpdateNodePosition(ctx, nodeID, model.Position{X: req.X, Y: req.Y}); err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) StartNodeHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	nodeID := strings.TrimSpace(r.PathValue("id"))
	if err := h.service.StartNode(ctx, nodeID); err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) StopNodeHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	nodeID := strings.TrimSpace(r.PathValue("id"))
	if err := h.service.stopNode(ctx, nodeID); err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) CLIHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	nodeID := strings.TrimSpace(r.PathValue("id"))

	var req commandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSONError(w, http.StatusBadRequest, "invalid command request")
		return
	}

	result, err := h.service.runCommand(ctx, nodeID, req.Command)
	if err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	_ = json.NewEncoder(w).Encode(result)
}
