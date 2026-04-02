package links

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/store"
)

type Handler struct {
	docker  *client.Client
	service *service
}

type createLinkRequest struct {
	NodeAID string `json:"nodeAId"`
	NodeBID string `json:"nodeBId"`
}

func NewHandler(docker *client.Client, store *store.Store) *Handler {
	return &Handler{docker: docker, service: newService(docker, store)}
}

func (h *Handler) CreateLinkHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	var req createLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	link, err := h.service.createLink(ctx, strings.TrimSpace(req.NodeAID), strings.TrimSpace(req.NodeBID))
	if err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(link)
}

func (h *Handler) ListLinksHandler(w http.ResponseWriter, r *http.Request) {
	links, err := h.service.listLinks()
	if err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	_ = json.NewEncoder(w).Encode(links)
}

func (h *Handler) DeleteLinkHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := httputil.WithRequestTimeout(r.Context())
	defer cancel()

	linkID := strings.TrimSpace(r.PathValue("id"))
	if err := h.service.deleteLink(ctx, linkID); err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
