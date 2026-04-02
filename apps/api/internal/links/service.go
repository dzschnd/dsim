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
	repo   *Repository
}

func newService(docker *client.Client, s *store.Store) *service {
	return &service{docker: docker, repo: NewRepository(s)}
}

func (s *service) createLink(ctx context.Context, interfaceAID, interfaceBID string) (model.Link, error) {
	if interfaceAID == "" || interfaceBID == "" {
		return model.Link{}, httputil.NewAppError(http.StatusBadRequest, "interface ids are required")
	}
	if interfaceAID == interfaceBID {
		return model.Link{}, httputil.NewAppError(http.StatusBadRequest, "interface ids must be different")
	}

	nodeA, ifaceA, ok := s.repo.GetNodeByInterface(interfaceAID)
	if !ok {
		return model.Link{}, httputil.NewAppError(http.StatusNotFound, "interface not found")
	}
	nodeB, ifaceB, ok := s.repo.GetNodeByInterface(interfaceBID)
	if !ok {
		return model.Link{}, httputil.NewAppError(http.StatusNotFound, "interface not found")
	}
	if nodeA.ID == nodeB.ID {
		return model.Link{}, httputil.NewAppError(http.StatusBadRequest, "interfaces must belong to different nodes")
	}
	if ifaceA.LinkID != "" || ifaceB.LinkID != "" {
		return model.Link{}, httputil.NewAppError(http.StatusConflict, "interface already connected")
	}
	if s.repo.HasLink(interfaceAID, interfaceBID) {
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

	inspectA, err := s.docker.ContainerInspect(ctx, nodeA.ContainerID)
	if err != nil {
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeB.ContainerID, true)
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeA.ContainerID, true)
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	endpointA, ok := inspectA.NetworkSettings.Networks[networkName]
	if !ok || endpointA == nil {
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeB.ContainerID, true)
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeA.ContainerID, true)
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "runtime network endpoint missing")
	}

	inspectB, err := s.docker.ContainerInspect(ctx, nodeB.ContainerID)
	if err != nil {
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeB.ContainerID, true)
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeA.ContainerID, true)
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	endpointB, ok := inspectB.NetworkSettings.Networks[networkName]
	if !ok || endpointB == nil {
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeB.ContainerID, true)
		_ = s.docker.NetworkDisconnect(ctx, networkResp.ID, nodeA.ContainerID, true)
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "runtime network endpoint missing")
	}

	link := model.Link{
		ID:           linkID,
		InterfaceAID: interfaceAID,
		InterfaceBID: interfaceBID,
		NetworkID:    networkResp.ID,
		NetworkName:  networkName,
		CreatedAt:    time.Now().UTC(),
	}
	s.repo.AddLink(link)
	s.repo.SetInterfaceLink(interfaceAID, linkID)
	s.repo.SetInterfaceLink(interfaceBID, linkID)
	s.repo.SetInterfaceRuntime(interfaceAID, endpointA.IPAddress, endpointA.IPPrefixLen)
	s.repo.SetInterfaceRuntime(interfaceBID, endpointB.IPAddress, endpointB.IPPrefixLen)

	return link, nil
}

func (s *service) listLinks() ([]model.Link, error) {
	return s.repo.ListLinks(), nil
}

func (s *service) deleteLink(ctx context.Context, linkID string) error {
	if linkID == "" {
		return httputil.NewAppError(http.StatusBadRequest, "link id required")
	}

	link, ok := s.repo.GetLink(linkID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "link not found")
	}

	s.removeLinkNetwork(ctx, link)
	s.repo.SetInterfaceLink(link.InterfaceAID, "")
	s.repo.SetInterfaceLink(link.InterfaceBID, "")
	s.repo.SetInterfaceRuntime(link.InterfaceAID, "", 0)
	s.repo.SetInterfaceRuntime(link.InterfaceBID, "", 0)
	s.repo.DeleteLink(linkID)
	return nil
}

func (s *service) removeLinkNetwork(ctx context.Context, link model.Link) {
	if link.NetworkID == "" {
		return
	}
	if nodeA, _, ok := s.repo.GetNodeByInterface(link.InterfaceAID); ok && nodeA.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeA.ContainerID, true)
	}
	if nodeB, _, ok := s.repo.GetNodeByInterface(link.InterfaceBID); ok && nodeB.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeB.ContainerID, true)
	}
	_ = s.docker.NetworkRemove(ctx, link.NetworkID)
}
