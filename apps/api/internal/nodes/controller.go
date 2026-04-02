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
	docker  *client.Client
	service *service
}

type createNodeRequest struct {
	Type string `json:"type"`
}

type createNodeResponse struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Type        model.NodeType `json:"type"`
	ContainerID string         `json:"containerId"`
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
	return &Handler{docker: docker, service: newService(docker, store)}
}

func (h *Handler) CreateNodeHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	var req createNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSONError(w, http.StatusBadRequest, "type not provided")
		return
	}

	node, err := h.service.createNode(ctx, req.Type)
	if err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createNodeResponse{
		ID:          node.ID,
		Name:        node.Name,
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

func (h *Handler) StartNodeHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	nodeID := strings.TrimSpace(r.PathValue("id"))
	if err := h.service.startNode(ctx, nodeID); err != nil {
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
