package nodes

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

type linkRepository interface {
	DeleteLinkByNode(nodeID string)
}

type service struct {
	docker   *client.Client
	repo     *repository
	linkRepo linkRepository
}

func newService(docker *client.Client, repo *repository, linkRepo linkRepository) *service {
	return &service{docker: docker, repo: repo, linkRepo: linkRepo}
}

func (s *service) getNodes() ([]model.Node, error) {
	return s.repo.ListNodes(), nil
}

func nodeTypeTag(t model.NodeType) string {
	var image string
	switch t {
	case model.Host:
		image = strings.TrimSpace(os.Getenv("HOST_IMAGE"))
	default:
		image = strings.TrimSpace(os.Getenv("HOST_IMAGE"))
	}
	return image
}

func (s *service) createNode(ctx context.Context, reqNodeType string) (model.Node, error) {
	nodeType, ok := model.NameNodeType[reqNodeType]
	if !ok {
		return model.Node{}, httputil.NewAppError(http.StatusBadRequest, "invalid node type")
	}
	image := nodeTypeTag(nodeType)

	if _, _, err := s.docker.ImageInspectWithRaw(ctx, image); err != nil {
		if client.IsErrNotFound(err) {
			return model.Node{}, httputil.NewAppError(http.StatusNotFound, "image not found: "+image)
		}
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "image inspect failed")
	}

	nodeID := store.NewID("node_")
	networkName := nodeID + "_isolated"
	networkResp, err := s.docker.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
	})
	if err != nil {
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "isolated network create failed")
	}

	initEnabled := true
	createResp, err := s.docker.ContainerCreate(
		ctx,
		&container.Config{Image: image},
		&container.HostConfig{Init: &initEnabled},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {},
			},
		},
		nil, "",
	)
	if err != nil {
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "container create failed")
	}

	inspect, err := s.docker.ContainerInspect(ctx, createResp.ID)
	if err != nil {
		_ = s.docker.ContainerRemove(ctx, createResp.ID, container.RemoveOptions{Force: true})
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	name := strings.TrimPrefix(inspect.Name, "/")
	node := model.Node{
		ID:          nodeID,
		Name:        name,
		Status:      model.Idle,
		Type:        nodeType,
		ContainerID: createResp.ID,
		CreatedAt:   time.Now().UTC(),
		Interfaces:  make([]model.Interface, 0),
	}

	switch nodeType {
	case model.Host, model.Switch, model.Router:
		node.Interfaces = append(node.Interfaces, model.Interface{
			ID:   store.NewID("iface_"),
			Name: "eth0",
		})
	}

	s.repo.AddNode(node)

	return node, nil
}

func (s *service) deleteNode(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return httputil.NewAppError(http.StatusBadRequest, "node id required")
	}

	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	if s.linkRepo != nil {
		s.linkRepo.DeleteLinkByNode(nodeID)
	}

	if node.ContainerID != "" {
		err := s.docker.ContainerRemove(ctx, node.ContainerID, container.RemoveOptions{Force: true})
		if err != nil && !client.IsErrNotFound(err) {
			return httputil.NewAppError(http.StatusInternalServerError, "container remove failed")
		}
	}

	s.repo.DeleteNode(nodeID)
	return nil
}

func (s *service) startNode(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return httputil.NewAppError(http.StatusBadRequest, "node id required")
	}

	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		return httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State != nil && inspect.State.Running {
		s.syncRuntimeInterfaces(nodeID, node.Interfaces, inspect)
		s.repo.UpdateNodeStatus(nodeID, model.Running)
		return nil
	}

	if err := s.docker.ContainerStart(ctx, node.ContainerID, container.StartOptions{}); err != nil {
		return httputil.NewAppError(http.StatusInternalServerError, "failed to start node")
	}

	inspectStarted, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		return httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	s.syncRuntimeInterfaces(nodeID, node.Interfaces, inspectStarted)

	s.repo.UpdateNodeStatus(nodeID, model.Running)
	return nil
}

func (s *service) syncRuntimeInterfaces(nodeID string, interfaces []model.Interface, inspect types.ContainerJSON) {
	if inspect.NetworkSettings == nil || inspect.NetworkSettings.Networks == nil {
		return
	}

	s.repo.store.Mu.RLock()
	linksByID := make(map[string]model.Link, len(s.repo.store.Links))
	for _, link := range s.repo.store.Links {
		linksByID[link.ID] = link
	}
	s.repo.store.Mu.RUnlock()

	for _, iface := range interfaces {
		if iface.LinkID == "" {
			continue
		}

		link, ok := linksByID[iface.LinkID]
		if !ok {
			continue
		}

		endpoint, ok := inspect.NetworkSettings.Networks[link.NetworkName]
		if !ok || endpoint == nil {
			continue
		}

		s.repo.UpdateInterfaceRuntime(nodeID, iface.ID, endpoint.IPAddress, endpoint.IPPrefixLen)
	}
}

func (s *service) stopNode(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return httputil.NewAppError(http.StatusBadRequest, "node id required")
	}

	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		return httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State != nil && !inspect.State.Running {
		s.repo.UpdateNodeStatus(nodeID, model.Idle)
		return nil
	}

	if err := s.docker.ContainerStop(ctx, node.ContainerID, container.StopOptions{}); err != nil {
		return httputil.NewAppError(http.StatusInternalServerError, "failed to stop node: "+err.Error())
	}

	s.repo.UpdateNodeStatus(nodeID, model.Idle)
	return nil
}

func (s *service) execCommand(ctx context.Context, containerID string, execCmd []string, command string) (commandResponse, error) {
	execResp, err := s.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          execCmd,
	})
	if err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "exec create failed")
	}

	attachResp, err := s.docker.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "exec attach failed")
	}
	defer attachResp.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "exec read failed")
	}

	execInspect, err := s.docker.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "exec inspect failed")
	}

	return commandResponse{
		Command:  command,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: execInspect.ExitCode,
	}, nil
}

func (s *service) runCommand(ctx context.Context, nodeID, command string) (commandResponse, error) {
	if nodeID == "" {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "node id required")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "command is required")
	}

	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State == nil || !inspect.State.Running {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "node is not running")
	}

	if command == "ip addr" {
		return s.runIPAddr(command, node), nil
	}

	fields := strings.Fields(command)
	if len(fields) == 2 && fields[0] == "ping" {
		return s.runPing(ctx, command, node)
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "set" {
		return s.runIPSet(command, nodeID, fields[2], fields[3])
	}

	return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "unsupported command: "+command)
}

func (s *service) runIPAddr(command string, node model.Node) commandResponse {
	lines := make([]string, 0, len(node.Interfaces))
	for _, iface := range node.Interfaces {
		if iface.IPAddr == "" || iface.PrefixLen == 0 {
			lines = append(lines, fmt.Sprintf("%s: unassigned", iface.Name))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s/%d", iface.Name, iface.IPAddr, iface.PrefixLen))
	}

	return commandResponse{
		Command:  command,
		Stdout:   strings.Join(lines, "\n"),
		Stderr:   "",
		ExitCode: 0,
	}
}

func (s *service) runPing(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetLogicalIP := fields[1]
	targetAddr, err := netip.ParseAddr(targetLogicalIP)
	if err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}

	s.repo.store.Mu.RLock()
	defer s.repo.store.Mu.RUnlock()

	for _, sourceIface := range node.Interfaces {
		if sourceIface.LinkID == "" || sourceIface.IPAddr == "" || sourceIface.PrefixLen == 0 {
			continue
		}

		sourceAddr, err := netip.ParseAddr(sourceIface.IPAddr)
		if err != nil {
			continue
		}

		prefix := netip.PrefixFrom(sourceAddr, sourceIface.PrefixLen)
		if !prefix.Contains(targetAddr) {
			continue
		}

		for _, link := range s.repo.store.Links {
			if link.ID != sourceIface.LinkID {
				continue
			}

			targetInterfaceID := link.InterfaceAID
			if targetInterfaceID == sourceIface.ID {
				targetInterfaceID = link.InterfaceBID
			}

			for _, candidateNode := range s.repo.store.Nodes {
				for _, candidateIface := range candidateNode.Interfaces {
					if candidateIface.ID != targetInterfaceID {
						continue
					}
					if candidateIface.IPAddr != targetLogicalIP {
						continue
					}
					if candidateIface.RuntimeIPAddr == "" {
						return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "target interface has no runtime ip")
					}
					return s.execCommand(
						ctx,
						node.ContainerID,
						[]string{"ping", "-c", "4", candidateIface.RuntimeIPAddr},
						command,
					)
				}
			}
		}
	}

	return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "target ip not found on a directly connected interface")
}

func (s *service) runIPSet(command, nodeID, interfaceName, cidr string) (commandResponse, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid interface address")
	}

	if !s.repo.UpdateInterfaceAddress(nodeID, interfaceName, prefix.Addr().String(), prefix.Bits()) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("%s set to %s", interfaceName, cidr),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}
