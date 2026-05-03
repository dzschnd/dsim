package links

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/docker/docker/client"
	runtimesync "github.com/dzschnd/dsim/internal/docker"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/store"
)

type Handler struct {
	docker  *client.Client
	store   *store.Store
	service *Service
}

type createLinkRequest struct {
	InterfaceAID string `json:"interfaceAId"`
	InterfaceBID string `json:"interfaceBId"`
}

func NewHandler(docker *client.Client, store *store.Store) *Handler {
	return &Handler{
		docker:  docker,
		store:   store,
		service: NewService(docker, store),
	}
}

func (h *Handler) CreateLinkHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req createLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	interfaceAID := strings.TrimSpace(req.InterfaceAID)
	interfaceBID := strings.TrimSpace(req.InterfaceBID)

	link, err := h.service.CreateLink(ctx, interfaceAID, interfaceBID)
	if err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	if syncErr := runtimesync.SyncRoutesForInterfaces(ctx, h.docker, h.store, []string{
		interfaceAID,
		interfaceBID,
	}); syncErr != nil {
		slog.Warn("Route sync failed after link creation", "link_id", link.ID, "err", syncErr)
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
	ctx := r.Context()

	linkID := strings.TrimSpace(r.PathValue("id"))
	if err := h.service.deleteLink(ctx, linkID); err != nil {
		httputil.WriteAppError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
