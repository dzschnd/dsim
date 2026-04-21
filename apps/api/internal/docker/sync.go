package docker

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

func SyncAllRoutes(ctx context.Context, docker *client.Client, topologyStore *store.Store) error {
	nodes := topologyStore.NodesSnapshot()
	nodeIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		nodeIDs = append(nodeIDs, node.ID)
	}

	return SyncRoutesForNodes(ctx, docker, topologyStore, nodeIDs)
}

func SyncRoutesForInterfaces(ctx context.Context, docker *client.Client, topologyStore *store.Store, interfaceIDs []string) error {
	topologyStore.Mu.RLock()
	nodeIDs := make([]string, 0, len(interfaceIDs))
	for _, interfaceID := range interfaceIDs {
		nodeID, ok := topologyStore.InterfaceOwnerIndex[interfaceID]
		if ok {
			nodeIDs = append(nodeIDs, nodeID)
		}
	}
	topologyStore.Mu.RUnlock()

	return SyncRoutesForNodes(ctx, docker, topologyStore, nodeIDs)
}

func SyncRoutesForNodes(ctx context.Context, docker *client.Client, topologyStore *store.Store, nodeIDs []string) error {
	seen := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if nodeID == "" {
			continue
		}
		if _, ok := seen[nodeID]; ok {
			continue
		}
		seen[nodeID] = struct{}{}

		if err := syncNodeRoutes(ctx, docker, topologyStore, nodeID); err != nil {
			return err
		}
	}

	return nil
}

func syncNodeRoutes(ctx context.Context, docker *client.Client, topologyStore *store.Store, nodeID string) error {
	topologyStore.Mu.RLock()
	node, ok := topologyStore.Nodes[nodeID]
	topologyStore.Mu.RUnlock()
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}
	if node.Type == model.Switch || node.Status != model.Running || node.ContainerID == "" {
		return nil
	}

	for _, route := range node.Routes {
		if err := applyRuntimeRoute(ctx, docker, topologyStore, node, route); err != nil {
			return err
		}
	}

	return nil
}

func applyRuntimeRoute(ctx context.Context, docker *client.Client, topologyStore *store.Store, node model.Node, route model.Route) error {
	topologyStore.Mu.RLock()
	_, _, ok := findReachableNextHopLocked(topologyStore, node, route.NextHop)
	topologyStore.Mu.RUnlock()
	if !ok {
		return httputil.NewAppError(http.StatusBadRequest, "next hop is not directly reachable")
	}

	destination := route.Destination
	if destination == "0.0.0.0/0" {
		destination = "default"
	}

	if _, err := execInContainerChecked(
		ctx,
		docker,
		node.ContainerID,
		[]string{"ip", "route", "replace", destination, "via", route.NextHop},
		"failed to apply runtime route",
	); err != nil {
		return err
	}

	return nil
}

func findReachableNextHopLocked(topologyStore *store.Store, node model.Node, nextHop string) (model.Node, model.Interface, bool) {
	nextHopAddr, err := netip.ParseAddr(nextHop)
	if err != nil {
		return model.Node{}, model.Interface{}, false
	}

	for _, sourceIface := range node.Interfaces {
		if sourceIface.LinkID == "" || sourceIface.IPAddr == "" || sourceIface.PrefixLen == 0 {
			continue
		}

		sourceAddr, err := netip.ParseAddr(sourceIface.IPAddr)
		if err != nil {
			continue
		}

		prefix := netip.PrefixFrom(sourceAddr, sourceIface.PrefixLen)
		if !prefix.Contains(nextHopAddr) {
			continue
		}

		peerNode, peerIface, ok := findInterfaceThroughSwitchesLocked(topologyStore, sourceIface.ID, map[string]struct{}{}, func(candidateNode model.Node, candidateIface model.Interface) bool {
			return candidateNode.Type != model.Switch && candidateIface.IPAddr == nextHop
		})
		if ok {
			return peerNode, peerIface, true
		}
	}

	return model.Node{}, model.Interface{}, false
}

func findInterfaceThroughSwitchesLocked(
	topologyStore *store.Store,
	interfaceID string,
	visited map[string]struct{},
	match func(model.Node, model.Interface) bool,
) (model.Node, model.Interface, bool) {
	if _, ok := visited[interfaceID]; ok {
		return model.Node{}, model.Interface{}, false
	}
	visited[interfaceID] = struct{}{}

	_, iface, ok := findInterfaceOwnerLocked(topologyStore, interfaceID)
	if !ok || iface.LinkID == "" {
		return model.Node{}, model.Interface{}, false
	}

	link, ok := topologyStore.Links[iface.LinkID]
	if !ok {
		return model.Node{}, model.Interface{}, false
	}

	peerInterfaceID := link.InterfaceAID
	if peerInterfaceID == iface.ID {
		peerInterfaceID = link.InterfaceBID
	}
	if _, ok := visited[peerInterfaceID]; ok {
		return model.Node{}, model.Interface{}, false
	}
	visited[peerInterfaceID] = struct{}{}

	peerNode, peerIface, ok := findInterfaceOwnerLocked(topologyStore, peerInterfaceID)
	if !ok {
		return model.Node{}, model.Interface{}, false
	}
	if peerNode.Type != model.Switch {
		if match(peerNode, peerIface) {
			return peerNode, peerIface, true
		}
		return model.Node{}, model.Interface{}, false
	}

	for _, switchIface := range peerNode.Interfaces {
		if switchIface.ID == peerIface.ID || switchIface.LinkID == "" {
			continue
		}
		foundNode, foundIface, found := findInterfaceThroughSwitchesLocked(topologyStore, switchIface.ID, visited, match)
		if found {
			return foundNode, foundIface, true
		}
	}

	return model.Node{}, model.Interface{}, false
}

func findInterfaceOwnerLocked(topologyStore *store.Store, interfaceID string) (model.Node, model.Interface, bool) {
	nodeID, ok := topologyStore.InterfaceOwnerIndex[interfaceID]
	if !ok {
		return model.Node{}, model.Interface{}, false
	}

	node, ok := topologyStore.Nodes[nodeID]
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

func execInContainer(ctx context.Context, docker *client.Client, containerID string, execCmd []string) (string, string, int, error) {
	execResp, err := docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          execCmd,
	})
	if err != nil {
		slog.Error("Exec create failed", "err", err)
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec create failed")
	}

	attachResp, err := docker.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		slog.Error("Exec attach failed", "err", err)
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec attach failed")
	}
	defer attachResp.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		slog.Error("Exec read failed", "err", err)
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec read failed")
	}

	execInspect, err := docker.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		slog.Error("Exec inspect failed", "err", err)
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec inspect failed")
	}

	return stdout.String(), stderr.String(), execInspect.ExitCode, nil
}

func execInContainerChecked(
	ctx context.Context,
	docker *client.Client,
	containerID string,
	execCmd []string,
	failureMessage string,
) (string, error) {
	stdout, stderr, exitCode, err := execInContainer(ctx, docker, containerID, execCmd)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		message := failureMessage
		if trimmed := strings.TrimSpace(stderr); trimmed != "" {
			message += ": " + trimmed
		}
		slog.Error("Container exec failed", "message", message)
		return "", httputil.NewAppError(http.StatusInternalServerError, message)
	}
	return stdout, nil
}
