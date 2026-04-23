package topology

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	runtimesync "github.com/dzschnd/dsim/internal/docker"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/links"
	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/nodes"
	"github.com/dzschnd/dsim/internal/store"
)

type service struct {
	docker      *client.Client
	store       *store.Store
	nodeService *nodes.Service
	linkService *links.Service
}

func newService(docker *client.Client, store *store.Store) *service {
	return &service{
		docker:      docker,
		store:       store,
		nodeService: nodes.NewService(docker, store),
		linkService: links.NewService(docker, store),
	}
}

func (s *service) ExportTopology() (File, error) {
	nodesSnapshot := s.store.NodesSnapshot()
	linksSnapshot := s.store.LinksSnapshot()

	file := File{
		Nodes: make([]Node, 0, len(nodesSnapshot)),
		Links: make([]Link, 0, len(linksSnapshot)),
	}

	for _, node := range nodesSnapshot {
		exportedNode := Node{
			ID:       node.ID,
			Type:     model.NodeTypeName[node.Type],
			Position: Position{X: node.Position.X, Y: node.Position.Y},
			Running:  node.Status == model.Running,
			Routes:   make([]Route, 0, len(node.Routes)),
		}

		exportedNode.Interfaces = make([]Interface, 0, len(node.Interfaces))
		for _, iface := range node.Interfaces {
			exportedInterface := Interface{Name: iface.Name}
			if iface.IPAddr != "" && iface.PrefixLen > 0 {
				exportedInterface.CIDR = iface.IPAddr + "/" + strconv.Itoa(iface.PrefixLen)
			}
			exportedNode.Interfaces = append(exportedNode.Interfaces, exportedInterface)
		}

		for _, route := range node.Routes {
			exportedNode.Routes = append(exportedNode.Routes, Route{
				Destination: route.Destination,
				NextHop:     route.NextHop,
			})
		}

		file.Nodes = append(file.Nodes, exportedNode)
	}

	for _, link := range linksSnapshot {
		nodeA, ifaceA, ok := s.nodeByInterface(link.InterfaceAID)
		if !ok {
			return File{}, httputil.NewAppError(http.StatusInternalServerError, "failed to resolve link endpoint A")
		}
		nodeB, ifaceB, ok := s.nodeByInterface(link.InterfaceBID)
		if !ok {
			return File{}, httputil.NewAppError(http.StatusInternalServerError, "failed to resolve link endpoint B")
		}

		file.Links = append(file.Links, Link{
			ID: link.ID,
			A: LinkEndpoint{
				NodeID:    nodeA.ID,
				Interface: ifaceA.Name,
			},
			B: LinkEndpoint{
				NodeID:    nodeB.ID,
				Interface: ifaceB.Name,
			},
		})
	}

	return file, nil
}

func (s *service) ImportTopology(ctx context.Context, file File) error {
	backup, err := s.ExportTopology()
	if err != nil {
		return err
	}

	if err := s.importTopologyUnsafe(ctx, file); err != nil {
		slog.Error("Topology import failed, attempting rollback", "err", err)

		CleanupRuntime(ctx, s.docker, s.store)
		if clearErr := ClearStore(ctx, s.docker, s.store); clearErr != nil {
			slog.Error("Topology rollback store reset failed", "err", clearErr)
			return httputil.NewAppError(http.StatusInternalServerError, "topology import failed and rollback store reset failed")
		}
		if rollbackErr := s.importTopologyUnsafe(ctx, backup); rollbackErr != nil {
			slog.Error("Topology rollback import failed", "err", rollbackErr)
			CleanupRuntime(ctx, s.docker, s.store)
			if finalClearErr := ClearStore(ctx, s.docker, s.store); finalClearErr != nil {
				slog.Error("Topology final cleanup after rollback failure failed", "err", finalClearErr)
				return httputil.NewAppError(http.StatusInternalServerError, "topology import failed, rollback failed, and final cleanup failed")
			}
			return httputil.NewAppError(http.StatusInternalServerError, "topology import failed and rollback failed; topology was cleared")
		}
		return err
	}

	return nil
}

func (s *service) importTopologyUnsafe(ctx context.Context, file File) error {
	CleanupRuntime(ctx, s.docker, s.store)
	if err := ClearStore(ctx, s.docker, s.store); err != nil {
		return err
	}

	nodeIDByFileID := make(map[string]string, len(file.Nodes))
	runningNodeIDs := make([]string, 0, len(file.Nodes))

	for _, topologyNode := range file.Nodes {
		createdNode, err := s.nodeService.CreateNode(ctx, topologyNode.Type, model.Position{
			X: topologyNode.Position.X,
			Y: topologyNode.Position.Y,
		})
		if err != nil {
			return err
		}

		if err := s.applyNodeConfig(createdNode.ID, topologyNode); err != nil {
			return err
		}

		nodeIDByFileID[topologyNode.ID] = createdNode.ID
		if topologyNode.Running {
			runningNodeIDs = append(runningNodeIDs, createdNode.ID)
		}
	}

	for _, topologyLink := range file.Links {
		nodeAID, ok := nodeIDByFileID[topologyLink.A.NodeID]
		if !ok {
			return httputil.NewAppError(http.StatusBadRequest, "link endpoint A node not found")
		}
		nodeBID, ok := nodeIDByFileID[topologyLink.B.NodeID]
		if !ok {
			return httputil.NewAppError(http.StatusBadRequest, "link endpoint B node not found")
		}

		interfaceAID, err := s.interfaceIDByName(nodeAID, topologyLink.A.Interface)
		if err != nil {
			return err
		}
		interfaceBID, err := s.interfaceIDByName(nodeBID, topologyLink.B.Interface)
		if err != nil {
			return err
		}

		if _, err := s.linkService.CreateLink(ctx, interfaceAID, interfaceBID); err != nil {
			return err
		}
	}

	for _, nodeID := range runningNodeIDs {
		if err := s.nodeService.StartNode(ctx, nodeID); err != nil {
			return err
		}
	}
	if err := runtimesync.SyncAllRoutes(ctx, s.docker, s.store); err != nil {
		return err
	}
	return nil
}

func CleanupRuntime(ctx context.Context, docker *client.Client, topologyStore *store.Store) {
	nodesSnapshot := topologyStore.NodesSnapshot()
	linksSnapshot := topologyStore.LinksSnapshot()

	const linkPoolSize = 6
	const nodePoolSize = 2 * linkPoolSize
	linkSem := make(chan struct{}, linkPoolSize)
	nodeSem := make(chan struct{}, nodePoolSize)
	var wg sync.WaitGroup

	for _, node := range nodesSnapshot {
		if node.ContainerID == "" && node.NetworkID == "" {
			continue
		}
		node := node
		wg.Go(func() {
			nodeSem <- struct{}{}
			defer func() { <-nodeSem }()

			if node.ContainerID != "" {
				if err := docker.ContainerRemove(ctx, node.ContainerID, container.RemoveOptions{Force: true}); err != nil && !client.IsErrNotFound(err) {
					slog.Error("Topology cleanup container remove failed", "container_id", node.ContainerID, "err", err)
				} else {
					slog.Info("Topology cleanup container removed", "container_id", node.ContainerID)
				}
			}

			if node.NetworkID != "" {
				if err := docker.NetworkRemove(ctx, node.NetworkID); err != nil && !client.IsErrNotFound(err) {
					slog.Error("Topology cleanup isolated network remove failed", "network_id", node.NetworkID, "err", err)
				} else {
					slog.Info("Topology cleanup isolated network removed", "network_id", node.NetworkID)
				}
			}
		})
	}
	wg.Wait()

	for _, link := range linksSnapshot {
		if link.NetworkID == "" {
			continue
		}
		link := link
		wg.Go(func() {
			linkSem <- struct{}{}
			defer func() { <-linkSem }()

			if err := docker.NetworkRemove(ctx, link.NetworkID); err != nil && !client.IsErrNotFound(err) {
				slog.Error("Topology cleanup link network remove failed", "network_id", link.NetworkID, "err", err)
			} else {
				slog.Info("Topology cleanup link network removed", "network_id", link.NetworkID)
			}
		})
	}
	wg.Wait()
}

func ClearStore(ctx context.Context, docker *client.Client, topologyStore *store.Store) error {
	freshStore, err := store.NewStore(ctx, docker)
	if err != nil {
		slog.Error("Topology store reset failed", "err", err)
		return err
	}

	topologyStore.Mu.Lock()
	defer topologyStore.Mu.Unlock()

	topologyStore.Nodes = freshStore.Nodes
	topologyStore.Links = freshStore.Links
	topologyStore.LinkIndex = freshStore.LinkIndex
	topologyStore.InterfaceOwnerIndex = freshStore.InterfaceOwnerIndex
	topologyStore.IsolatedSubnets = freshStore.IsolatedSubnets
	topologyStore.LinkSubnets = freshStore.LinkSubnets

	return nil
}

func (s *service) applyNodeConfig(nodeID string, topologyNode Node) error {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	node, ok := s.store.Nodes[nodeID]
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found during topology import")
	}

	node.Routes = make([]model.Route, 0, len(topologyNode.Routes))

	for _, topologyInterface := range topologyNode.Interfaces {
		if topologyInterface.CIDR == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(topologyInterface.CIDR)
		if err != nil {
			return httputil.NewAppError(http.StatusBadRequest, "invalid interface cidr")
		}

		found := false
		for index, iface := range node.Interfaces {
			if iface.Name != topologyInterface.Name {
				continue
			}
			node.Interfaces[index].IPAddr = prefix.Addr().String()
			node.Interfaces[index].PrefixLen = prefix.Bits()
			found = true
			break
		}
		if !found {
			return httputil.NewAppError(http.StatusBadRequest, "interface not found during topology import")
		}
	}

	for _, topologyRoute := range topologyNode.Routes {
		node.Routes = append(node.Routes, model.Route{
			Destination: topologyRoute.Destination,
			NextHop:     topologyRoute.NextHop,
		})
	}

	s.store.Nodes[nodeID] = node
	return nil
}

func (s *service) interfaceIDByName(nodeID, interfaceName string) (string, error) {
	s.store.Mu.RLock()
	defer s.store.Mu.RUnlock()

	node, ok := s.store.Nodes[nodeID]
	if !ok {
		return "", httputil.NewAppError(http.StatusNotFound, "node not found during link import")
	}

	for _, iface := range node.Interfaces {
		if iface.Name == interfaceName {
			return iface.ID, nil
		}
	}

	return "", httputil.NewAppError(http.StatusBadRequest, "interface not found during link import")
}

func (s *service) nodeByInterface(interfaceID string) (model.Node, model.Interface, bool) {
	s.store.Mu.RLock()
	defer s.store.Mu.RUnlock()

	nodeID, ok := s.store.InterfaceOwnerIndex[interfaceID]
	if !ok {
		return model.Node{}, model.Interface{}, false
	}

	node, ok := s.store.Nodes[nodeID]
	if !ok {
		return model.Node{}, model.Interface{}, false
	}

	for _, iface := range node.Interfaces {
		if iface.ID == interfaceID {
			return node, iface, true
		}
	}

	return model.Node{}, model.Interface{}, false
}
