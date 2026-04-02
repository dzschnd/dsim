package links

import (
	"context"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

type service struct {
	docker *client.Client
	repo   *repository
}

func newService(docker *client.Client, s *store.Store) *service {
	return &service{docker: docker, repo: newRepository(s)}
}

func (s *service) checkStoreExists() error {
	if s.repo.store == nil {
		return httputil.NewAppError(http.StatusInternalServerError, "store not initialized")
	}
	return nil
}

func (s *service) createLink(ctx context.Context, nodeAID, nodeBID string) (model.Link, error) {
	if err := s.checkStoreExists(); err != nil {
		return model.Link{}, err
	}
	if nodeAID == "" || nodeBID == "" {
		return model.Link{}, httputil.NewAppError(http.StatusBadRequest, "node ids are required")
	}
	if nodeAID == nodeBID {
		return model.Link{}, httputil.NewAppError(http.StatusBadRequest, "node ids must be different")
	}

	nodeA, ok := s.repo.GetNode(nodeAID)
	if !ok {
		return model.Link{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}
	nodeB, ok := s.repo.GetNode(nodeBID)
	if !ok {
		return model.Link{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}
	if s.repo.HasLink(nodeAID, nodeBID) {
		return model.Link{}, httputil.NewAppError(http.StatusConflict, "link already exists")
	}
	if nodeA.ContainerID == "" || nodeB.ContainerID == "" {
		return model.Link{}, httputil.NewAppError(http.StatusBadRequest, "node container not set")
	}

	linkID := store.NewID("link_")
	networkName := linkID
	networkResp, err := s.docker.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
	})
	if err != nil {
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "network create failed")
	}

	if err := s.docker.NetworkConnect(ctx, networkResp.ID, nodeA.ContainerID, nil); err != nil {
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "network connect failed")
	}
	if err := s.docker.NetworkConnect(ctx, networkResp.ID, nodeB.ContainerID, nil); err != nil {
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeA.ContainerID, true)
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "network connect failed")
	}

	link := model.Link{
		ID:          linkID,
		NodeAID:     nodeAID,
		NodeBID:     nodeBID,
		NetworkID:   networkResp.ID,
		NetworkName: networkName,
		CreatedAt:   time.Now().UTC(),
	}
	s.repo.AddLink(link)

	return link, nil
}

func (s *service) listLinks() ([]model.Link, error) {
	if err := s.checkStoreExists(); err != nil {
		return nil, err
	}
	return s.repo.ListLinks(), nil
}

func (s *service) deleteLink(ctx context.Context, linkID string) error {
	if err := s.checkStoreExists(); err != nil {
		return err
	}
	if linkID == "" {
		return httputil.NewAppError(http.StatusBadRequest, "link id required")
	}

	link, ok := s.repo.GetLink(linkID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "link not found")
	}

	s.removeLinkNetwork(ctx, link)
	s.repo.DeleteLink(linkID)
	return nil
}

func (s *service) removeLinkNetwork(ctx context.Context, link model.Link) {
	if link.NetworkID == "" {
		return
	}
	if nodeA, ok := s.repo.GetNode(link.NodeAID); ok && nodeA.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeA.ContainerID, true)
	}
	if nodeB, ok := s.repo.GetNode(link.NodeBID); ok && nodeB.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeB.ContainerID, true)
	}
	_ = s.docker.NetworkRemove(ctx, link.NetworkID)
}
