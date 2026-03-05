package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/network"
)

type createLinkRequest struct {
	NodeAID string `json:"nodeAId"`
	NodeBID string `json:"nodeBId"`
}

func (s *Server) createLinkHandler(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONError(w, http.StatusInternalServerError, "store not initialized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var req createLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req.NodeAID = strings.TrimSpace(req.NodeAID)
	req.NodeBID = strings.TrimSpace(req.NodeBID)
	if req.NodeAID == "" || req.NodeBID == "" {
		writeJSONError(w, http.StatusBadRequest, "node ids are required")
		return
	}
	if req.NodeAID == req.NodeBID {
		writeJSONError(w, http.StatusBadRequest, "node ids must be different")
		return
	}
	nodeA, ok := s.store.GetNode(req.NodeAID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "node not found")
		return
	}
	nodeB, ok := s.store.GetNode(req.NodeBID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "node not found")
		return
	}
	if s.store.HasLink(req.NodeAID, req.NodeBID) {
		writeJSONError(w, http.StatusConflict, "link already exists")
		return
	}
	if nodeA.ContainerID == "" || nodeB.ContainerID == "" {
		writeJSONError(w, http.StatusBadRequest, "node container not set")
		return
	}

	linkID := newID("link_")
	networkName := linkID
	networkResp, err := s.docker.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "network create failed")
		return
	}

	if err := s.docker.NetworkConnect(ctx, networkResp.ID, nodeA.ContainerID, nil); err != nil {
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		writeJSONError(w, http.StatusInternalServerError, "network connect failed")
		return
	}
	if err := s.docker.NetworkConnect(ctx, networkResp.ID, nodeB.ContainerID, nil); err != nil {
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeA.ContainerID, true)
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		writeJSONError(w, http.StatusInternalServerError, "network connect failed")
		return
	}

	link := Link{
		ID:          linkID,
		NodeAID:     req.NodeAID,
		NodeBID:     req.NodeBID,
		NetworkID:   networkResp.ID,
		NetworkName: networkName,
		CreatedAt:   time.Now().UTC(),
	}
	s.store.AddLink(link)

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(link)
}

func (s *Server) listLinksHandler(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONError(w, http.StatusInternalServerError, "store not initialized")
		return
	}
	links := s.store.ListLinks()
	_ = json.NewEncoder(w).Encode(links)
}

func (s *Server) deleteLinkHandler(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONError(w, http.StatusInternalServerError, "store not initialized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	linkID := strings.TrimSpace(r.PathValue("id"))
	if linkID == "" {
		writeJSONError(w, http.StatusBadRequest, "link id required")
		return
	}
	link, ok := s.store.GetLink(linkID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "link not found")
		return
	}

	s.removeLinkNetwork(ctx, link)

	s.store.DeleteLink(linkID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeLinkNetwork(ctx context.Context, link Link) {
	if link.NetworkID == "" {
		return
	}
	if nodeA, ok := s.store.GetNode(link.NodeAID); ok && nodeA.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeA.ContainerID, true)
	}
	if nodeB, ok := s.store.GetNode(link.NodeBID); ok && nodeB.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeB.ContainerID, true)
	}
	_ = s.docker.NetworkRemove(ctx, link.NetworkID)
}
