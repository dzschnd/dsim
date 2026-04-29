package topology

import (
	"encoding/json"
	"net/http"

	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/store"
)

type Handler struct {
	service *service
}

func NewHandler(docker *client.Client, store *store.Store) *Handler {
	return &Handler{service: newService(docker, store)}
}

func (h *Handler) ExportTopologyHandler(w http.ResponseWriter, r *http.Request) {
	topology, err := h.service.ExportTopology()
	if err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	_ = json.NewEncoder(w).Encode(topology)
}

func (h *Handler) ImportTopologyHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var file File
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		httputil.WriteJSONError(w, http.StatusBadRequest, "invalid topology payload")
		return
	}

	if err := h.service.ImportTopology(ctx, file); err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ClearTopologyHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := h.service.ClearTopology(ctx); err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
