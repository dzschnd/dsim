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
			Name:     node.Name,
			Type:     model.NodeTypeName[node.Type],
			Position: Position{X: node.Position.X, Y: node.Position.Y},
			Routes:   make([]Route, 0, len(node.Routes)),
		}
		if exportedNode.Name == "" {
			exportedNode.Name = node.ID
		}

		exportedNode.Interfaces = make([]Interface, 0, len(node.Interfaces))
		for _, iface := range node.Interfaces {
			exportedInterface := Interface{
				Name:       iface.Name,
				Conditions: iface.Conditions,
			}
			if iface.IPAddr != "" && iface.PrefixLen > 0 {
				exportedInterface.CIDR = iface.IPAddr + "/" + strconv.Itoa(iface.PrefixLen)
			}
			exportedNode.Interfaces = append(exportedNode.Interfaces, exportedInterface)
		}

		for _, route := range node.Routes {
			exportedNode.Routes = append(exportedNode.Routes, Route{
				Destination: route.Destination,
				NextHop:     route.NextHop,
				Kind:        string(route.Kind),
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

func (s *service) ClearTopology(ctx context.Context) error {
	CleanupRuntime(ctx, s.docker, s.store)
	return ClearStore(ctx, s.docker, s.store)
}

func (s *service) importTopologyUnsafe(ctx context.Context, file File) error {
	const importWorkers = 16

	CleanupRuntime(ctx, s.docker, s.store)
	if err := ClearStore(ctx, s.docker, s.store); err != nil {
		return err
	}

	nodeIDByFileID, err := s.importNodesParallel(ctx, file.Nodes, importWorkers)
	if err != nil {
		return err
	}

	if err := s.importLinksParallel(ctx, file.Links, nodeIDByFileID, importWorkers); err != nil {
		return err
	}

	if err := runtimesync.SyncAllRoutes(ctx, s.docker, s.store); err != nil {
		return err
	}
	return nil
}

func (s *service) importNodesParallel(ctx context.Context, topologyNodes []Node, workers int) (map[string]string, error) {
	type task struct {
		index int
		node  Node
	}
	type result struct {
		fileID    string
		createdID string
	}

	nodeIDByFileID := make(map[string]string, len(topologyNodes))
	if len(topologyNodes) == 0 {
		return nodeIDByFileID, nil
	}

	workerCount := workers
	if len(topologyNodes) < workerCount {
		workerCount = len(topologyNodes)
	}

	taskCh := make(chan task)
	errs := make([]error, len(topologyNodes))
	results := make([]result, len(topologyNodes))
	var wg sync.WaitGroup
	var resultsMu sync.Mutex

	wg.Add(workerCount)
	for range workerCount {
		go func() {
			defer wg.Done()
			for item := range taskCh {
				createdNode, err := s.nodeService.CreateNode(ctx, item.node.Type, model.Position{
					X: item.node.Position.X,
					Y: item.node.Position.Y,
				})
				if err != nil {
					errs[item.index] = err
					continue
				}
				if err := s.applyNodeConfig(createdNode.ID, item.node); err != nil {
					errs[item.index] = err
					continue
				}

				resultsMu.Lock()
				results[item.index] = result{
					fileID:    item.node.ID,
					createdID: createdNode.ID,
				}
				resultsMu.Unlock()
			}
		}()
	}

	for i, topologyNode := range topologyNodes {
		taskCh <- task{index: i, node: topologyNode}
	}
	close(taskCh)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, err
		}
		entry := results[i]
		nodeIDByFileID[entry.fileID] = entry.createdID
	}

	return nodeIDByFileID, nil
}

func (s *service) importLinksParallel(ctx context.Context, topologyLinks []Link, nodeIDByFileID map[string]string, workers int) error {
	type task struct {
		index int
		link  Link
	}

	if len(topologyLinks) == 0 {
		return nil
	}

	workerCount := workers
	if len(topologyLinks) < workerCount {
		workerCount = len(topologyLinks)
	}

	taskCh := make(chan task)
	errs := make([]error, len(topologyLinks))
	var wg sync.WaitGroup

	wg.Add(workerCount)
	for range workerCount {
		go func() {
			defer wg.Done()
			for item := range taskCh {
				nodeAID, ok := nodeIDByFileID[item.link.A.NodeID]
				if !ok {
					errs[item.index] = httputil.NewAppError(http.StatusBadRequest, "link endpoint A node not found")
					continue
				}
				nodeBID, ok := nodeIDByFileID[item.link.B.NodeID]
				if !ok {
					errs[item.index] = httputil.NewAppError(http.StatusBadRequest, "link endpoint B node not found")
					continue
				}

				interfaceAID, err := s.interfaceIDByName(nodeAID, item.link.A.Interface)
				if err != nil {
					errs[item.index] = err
					continue
				}
				interfaceBID, err := s.interfaceIDByName(nodeBID, item.link.B.Interface)
				if err != nil {
					errs[item.index] = err
					continue
				}
				if _, err := s.linkService.CreateLink(ctx, interfaceAID, interfaceBID); err != nil {
					errs[item.index] = err
				}
			}
		}()
	}

	for i, topologyLink := range topologyLinks {
		taskCh <- task{index: i, link: topologyLink}
	}
	close(taskCh)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
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
	node.Name = topologyNode.Name

	for _, topologyInterface := range topologyNode.Interfaces {
		found := false
		for index, iface := range node.Interfaces {
			if iface.Name != topologyInterface.Name {
				continue
			}
			if err := nodes.ValidateTrafficConditions(topologyInterface.Conditions); err != nil {
				return err
			}
			if topologyInterface.CIDR != "" {
				prefix, err := netip.ParsePrefix(topologyInterface.CIDR)
				if err != nil {
					return httputil.NewAppError(http.StatusBadRequest, "invalid interface cidr")
				}
				node.Interfaces[index].IPAddr = prefix.Addr().String()
				node.Interfaces[index].PrefixLen = prefix.Bits()
			}
			node.Interfaces[index].Conditions = topologyInterface.Conditions
			found = true
			break
		}
		if !found {
			return httputil.NewAppError(http.StatusBadRequest, "interface not found during topology import")
		}
	}

	for _, topologyRoute := range topologyNode.Routes {
		routeKind := model.RouteKind(topologyRoute.Kind)
		if routeKind == "" {
			routeKind = model.RouteKindVia
		}
		switch routeKind {
		case model.RouteKindVia:
			if topologyRoute.NextHop == "" {
				return httputil.NewAppError(http.StatusBadRequest, "route next hop required")
			}
		case model.RouteKindBlackhole:
			if topologyRoute.NextHop != "" {
				return httputil.NewAppError(http.StatusBadRequest, "blackhole route cannot have next hop")
			}
		default:
			return httputil.NewAppError(http.StatusBadRequest, "invalid route kind")
		}
		node.Routes = append(node.Routes, model.Route{
			Destination: topologyRoute.Destination,
			NextHop:     topologyRoute.NextHop,
			Kind:        routeKind,
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
