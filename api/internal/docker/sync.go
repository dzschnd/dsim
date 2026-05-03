package docker

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
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
		if err := applyRuntimeRoute(ctx, docker, node, route); err != nil {
			return err
		}
	}

	return nil
}

func applyRuntimeRoute(ctx context.Context, docker *client.Client, node model.Node, route model.Route) error {
	destination := route.Destination
	if destination == "0.0.0.0/0" {
		destination = "default"
	}

	var execCmd []string
	switch route.Kind {
	case model.RouteKindVia:
		execCmd = []string{"ip", "route", "replace", destination, "via", route.NextHop}
	case model.RouteKindBlackhole:
		execCmd = []string{"ip", "route", "replace", "blackhole", destination}
	default:
		return httputil.NewAppError(http.StatusBadRequest, "invalid route kind")
	}

	if _, err := execInContainerChecked(
		ctx,
		docker,
		node.ContainerID,
		execCmd,
		"failed to apply runtime route",
	); err != nil {
		return err
	}

	return nil
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
