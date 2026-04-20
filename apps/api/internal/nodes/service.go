package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	"github.com/dzschnd/dsim/internal/links"
	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

type linkRepository interface {
	DeleteLinkByNode(nodeID string)
}

type Service struct {
	docker   *client.Client
	repo     *repository
	linkRepo linkRepository
}

const iperfLogPath = "/var/log/iperf/iperf.log"

func NewService(docker *client.Client, s *store.Store) *Service {
	repo := newRepository(s)
	linkRepo := links.NewRepository(s)
	return &Service{docker: docker, repo: repo, linkRepo: linkRepo}
}

func (s *Service) getNodes() ([]model.Node, error) {
	return s.repo.ListNodes(), nil
}

// TODO: add error handling for invalid type
func nodeTypeTag(t model.NodeType) (string, int) {
	var image string
	var ifaceCount int
	switch t {
	case model.Host:
		image = strings.TrimSpace(os.Getenv("HOST_IMAGE"))
		ifaceCount = 1
	case model.Switch:
		image = strings.TrimSpace(os.Getenv("HOST_IMAGE"))
		ifaceCount = 8
	case model.Router:
		image = strings.TrimSpace(os.Getenv("HOST_IMAGE"))
		ifaceCount = 4
	default:
		image = strings.TrimSpace(os.Getenv("HOST_IMAGE"))
		ifaceCount = 1
	}
	return image, ifaceCount
}

func (s *Service) CreateNode(ctx context.Context, reqNodeType string, position model.Position) (model.Node, error) {
	nodeType, ok := model.NameNodeType[reqNodeType]
	if !ok {
		return model.Node{}, httputil.NewAppError(http.StatusBadRequest, "invalid node type")
	}
	image, ifaceCount := nodeTypeTag(nodeType)

	if _, _, err := s.docker.ImageInspectWithRaw(ctx, image); err != nil {
		if client.IsErrNotFound(err) {
			return model.Node{}, httputil.NewAppError(http.StatusNotFound, "image not found: "+image)
		}
		slog.Error("Image inspect failed", "err", err)
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "image inspect failed")
	}

	nodeID := store.NewID("node_")
	networkName := nodeID + "_isolated"
	subnet, err := s.repo.store.IsolatedSubnets.Allocate()
	if err != nil {
		slog.Error("Isolated subnet allocation failed", "err", err)
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "isolated subnet allocation failed")
	}
	gateway, err := store.GatewayAddr(subnet)
	if err != nil {
		s.repo.store.IsolatedSubnets.Release(subnet)
		slog.Error("Isolated subnet gateway resolution failed", "err", err)
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "isolated subnet gateway resolution failed")
	}
	networkResp, err := s.docker.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{
					Subnet:  subnet.String(),
					Gateway: gateway.String(),
				},
			},
		},
	})
	if err != nil {
		s.repo.store.IsolatedSubnets.Release(subnet)
		slog.Error("Isolated network create failed", "err", err)
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "isolated network create failed")
	}

	initEnabled := true
	hostConfig := &container.HostConfig{
		Init:   &initEnabled,
		CapAdd: []string{"NET_ADMIN"},
	}
	if nodeType == model.Router {
		hostConfig.Sysctls = map[string]string{
			"net.ipv4.ip_forward": "1",
		}
	}
	createResp, err := s.docker.ContainerCreate(
		ctx,
		&container.Config{Image: image},
		hostConfig,
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {},
			},
		},
		nil, "",
	)
	if err != nil {
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		s.repo.store.IsolatedSubnets.Release(subnet)
		slog.Error("Container create failed", "err", err)
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "container create failed")
	}

	inspect, err := s.docker.ContainerInspect(ctx, createResp.ID)
	if err != nil {
		_ = s.docker.ContainerRemove(ctx, createResp.ID, container.RemoveOptions{Force: true})
		_ = s.docker.NetworkRemove(ctx, networkResp.ID)
		s.repo.store.IsolatedSubnets.Release(subnet)
		slog.Error("Container inspect failed", "err", err)
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	name := strings.TrimPrefix(inspect.Name, "/")
	node := model.Node{
		ID:          nodeID,
		Name:        name,
		Position:    position,
		Status:      model.Idle,
		Type:        nodeType,
		ContainerID: createResp.ID,
		NetworkID:   networkResp.ID,
		NetworkName: networkName,
		Subnet:      subnet.String(),
		CreatedAt:   time.Now().UTC(),
		Interfaces:  make([]model.Interface, 0, ifaceCount),
		Routes:      make([]model.Route, 0),
	}

	for i := 0; i < ifaceCount; i++ {
		node.Interfaces = append(node.Interfaces, model.Interface{
			ID:   store.NewID("iface_"),
			Name: fmt.Sprintf("eth%d", i),
		})
	}

	s.repo.AddNode(node)

	return node, nil
}

func (s *Service) UpdateNodePosition(ctx context.Context, nodeID string, position model.Position) error {
	_ = ctx

	if nodeID == "" {
		return httputil.NewAppError(http.StatusBadRequest, "node id required")
	}

	if !s.repo.UpdateNodePosition(nodeID, position) {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	return nil
}

func (s *Service) deleteNode(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return httputil.NewAppError(http.StatusBadRequest, "node id required")
	}

	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	links := s.linksForNode(nodeID)
	for _, link := range links {
		if err := s.removeLinkNetwork(ctx, link); err != nil {
			return err
		}
		s.releaseLinkSubnet(link)
		s.deleteLinkState(link)
	}

	if node.ContainerID != "" {
		err := s.docker.ContainerRemove(ctx, node.ContainerID, container.RemoveOptions{Force: true})
		if err != nil && !client.IsErrNotFound(err) {
			slog.Error("Container remove failed", "err", err)
			return httputil.NewAppError(http.StatusInternalServerError, "container remove failed")
		}
	}
	if node.NetworkID != "" {
		if err := s.docker.NetworkRemove(ctx, node.NetworkID); err != nil && !client.IsErrNotFound(err) {
			slog.Error("Isolated network remove failed", "err", err)
			return httputil.NewAppError(http.StatusInternalServerError, "isolated network remove failed")
		}
	}
	if node.Subnet != "" {
		s.repo.store.IsolatedSubnets.ReleaseString(node.Subnet)
	}

	s.repo.DeleteNode(nodeID)
	return nil
}

func (s *Service) linksForNode(nodeID string) []model.Link {
	s.repo.store.Mu.RLock()
	defer s.repo.store.Mu.RUnlock()

	links := make([]model.Link, 0)
	for _, link := range s.repo.store.Links {
		if s.nodeOwnsInterfaceLocked(nodeID, link.InterfaceAID) || s.nodeOwnsInterfaceLocked(nodeID, link.InterfaceBID) {
			links = append(links, link)
		}
	}
	return links
}

func (s *Service) nodeOwnsInterfaceLocked(nodeID, interfaceID string) bool {
	ownerID, ok := s.repo.store.InterfaceOwnerIndex[interfaceID]
	return ok && ownerID == nodeID
}

func (s *Service) removeLinkNetwork(ctx context.Context, link model.Link) error {
	if link.NetworkID == "" {
		return nil
	}
	if nodeA, _, ok := s.nodeByInterface(link.InterfaceAID); ok && nodeA.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeA.ContainerID, true)
	}
	if nodeB, _, ok := s.nodeByInterface(link.InterfaceBID); ok && nodeB.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeB.ContainerID, true)
	}
	if err := s.docker.NetworkRemove(ctx, link.NetworkID); err != nil && !client.IsErrNotFound(err) {
		slog.Error("Link network remove failed", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "link network remove failed")
	}
	return nil
}

func (s *Service) nodeByInterface(interfaceID string) (model.Node, model.Interface, bool) {
	s.repo.store.Mu.RLock()
	defer s.repo.store.Mu.RUnlock()

	nodeID, ok := s.repo.store.InterfaceOwnerIndex[interfaceID]
	if !ok {
		return model.Node{}, model.Interface{}, false
	}
	node, ok := s.repo.store.Nodes[nodeID]
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

func (s *Service) releaseLinkSubnet(link model.Link) {
	if link.Subnet == "" {
		return
	}
	s.repo.store.LinkSubnets.ReleaseString(link.Subnet)
}

func (s *Service) deleteLinkState(link model.Link) {
	s.repo.store.Mu.Lock()
	defer s.repo.store.Mu.Unlock()

	s.setInterfaceLinkStateLocked(link.InterfaceAID, "", "", 0, "")
	s.setInterfaceLinkStateLocked(link.InterfaceBID, "", "", 0, "")
	delete(s.repo.store.Links, link.ID)
	delete(s.repo.store.LinkIndex, nodeLinkKey(link.InterfaceAID, link.InterfaceBID))
}

func (s *Service) setInterfaceLinkStateLocked(interfaceID, linkID, runtimeIP string, runtimePrefixLen int, runtimeName string) {
	nodeID, ok := s.repo.store.InterfaceOwnerIndex[interfaceID]
	if !ok {
		return
	}
	node, ok := s.repo.store.Nodes[nodeID]
	if !ok {
		return
	}
	for index, iface := range node.Interfaces {
		if iface.ID != interfaceID {
			continue
		}
		node.Interfaces[index].LinkID = linkID
		node.Interfaces[index].RuntimeIPAddr = runtimeIP
		node.Interfaces[index].RuntimePrefixLen = runtimePrefixLen
		node.Interfaces[index].RuntimeName = runtimeName
		s.repo.store.Nodes[nodeID] = node
		return
	}
}

func nodeLinkKey(a, b string) string {
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}

func (s *Service) StartNode(ctx context.Context, nodeID string) error {
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
		slog.Error("Container inspect failed", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State != nil && inspect.State.Running {
		s.syncRuntimeInterfaces(nodeID, node.Interfaces, inspect)
		if err := s.syncRuntimeInterfaceNames(ctx, nodeID); err != nil {
			return err
		}
		if node.Type == model.Switch {
			if err := s.syncSwitchPorts(ctx, nodeID); err != nil {
				return err
			}
		}
		if err := s.syncRuntimeInterfaceAddresses(ctx, nodeID); err != nil {
			return err
		}
		if err := s.syncRuntimeRoutes(ctx, nodeID); err != nil {
			return err
		}
		s.repo.UpdateNodeStatus(nodeID, model.Running)
		return nil
	}

	if err := s.docker.ContainerStart(ctx, node.ContainerID, container.StartOptions{}); err != nil {
		slog.Error("Failed to start node", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "failed to start node")
	}

	inspectStarted, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		slog.Error("Container inspect failed", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	s.syncRuntimeInterfaces(nodeID, node.Interfaces, inspectStarted)
	if err := s.syncRuntimeInterfaceNames(ctx, nodeID); err != nil {
		return err
	}
	if node.Type == model.Switch {
		if err := s.syncSwitchPorts(ctx, nodeID); err != nil {
			return err
		}
	}
	if err := s.syncRuntimeInterfaceAddresses(ctx, nodeID); err != nil {
		return err
	}
	if err := s.syncRuntimeRoutes(ctx, nodeID); err != nil {
		return err
	}

	s.repo.UpdateNodeStatus(nodeID, model.Running)

	return nil
}

func (s *Service) ensureSwitchBridge(ctx context.Context, node model.Node) error {
	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"sh", "-c", "ip link show br0 >/dev/null 2>&1 || ip link add br0 type bridge"},
		"failed to create switch bridge",
	); err != nil {
		return err
	}

	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "link", "set", "br0", "up"},
		"failed to bring switch bridge up",
	); err != nil {
		return err
	}

	return nil
}

func (s *Service) attachSwitchPort(ctx context.Context, node model.Node, runtimeName string) error {
	if runtimeName == "" {
		return httputil.NewAppError(http.StatusBadRequest, "runtime interface name not resolved")
	}
	if err := s.ensureSwitchBridge(ctx, node); err != nil {
		return err
	}

	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "link", "set", runtimeName, "master", "br0"},
		"failed to attach switch port to bridge",
	); err != nil {
		return err
	}

	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "link", "set", runtimeName, "up"},
		"failed to bring switch port up",
	); err != nil {
		return err
	}

	return nil
}

func (s *Service) syncSwitchPorts(ctx context.Context, nodeID string) error {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}
	if node.Type != model.Switch {
		return nil
	}
	if err := s.ensureSwitchBridge(ctx, node); err != nil {
		return err
	}

	for _, iface := range node.Interfaces {
		if iface.LinkID == "" || iface.RuntimeName == "" {
			continue
		}
		if err := s.attachSwitchPort(ctx, node, iface.RuntimeName); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) syncRuntimeInterfaces(nodeID string, interfaces []model.Interface, inspect types.ContainerJSON) {
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

type runtimeAddrInfo struct {
	Local     string `json:"local"`
	PrefixLen int    `json:"prefixlen"`
}

type runtimeInterfaceInfo struct {
	IfName   string            `json:"ifname"`
	AddrInfo []runtimeAddrInfo `json:"addr_info"`
}

func (s *Service) syncRuntimeInterfaceNames(ctx context.Context, nodeID string) error {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	stdout, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "-j", "addr", "show"},
		"failed to inspect runtime interfaces",
	)
	if err != nil {
		return err
	}

	var runtimeIfaces []runtimeInterfaceInfo
	if err := json.Unmarshal([]byte(stdout), &runtimeIfaces); err != nil {
		slog.Error("Runtime interface inspect parse failed", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "runtime interface inspect parse failed")
	}

	runtimeNameByAddress := make(map[string]string, len(runtimeIfaces))
	for _, runtimeIface := range runtimeIfaces {
		for _, addrInfo := range runtimeIface.AddrInfo {
			key := fmt.Sprintf("%s/%d", addrInfo.Local, addrInfo.PrefixLen)
			runtimeNameByAddress[key] = runtimeIface.IfName
		}
	}

	for _, iface := range node.Interfaces {
		if iface.RuntimeName != "" {
			continue
		}
		if iface.RuntimeIPAddr == "" || iface.RuntimePrefixLen == 0 {
			continue
		}

		addr := fmt.Sprintf("%s/%d", iface.RuntimeIPAddr, iface.RuntimePrefixLen)
		runtimeName := runtimeNameByAddress[addr]
		if runtimeName == "" {
			slog.Error("Runtime interface name resolution failed")
			return httputil.NewAppError(http.StatusInternalServerError, "runtime interface name resolution failed")
		}
		if !s.repo.UpdateInterfaceRuntimeName(nodeID, iface.ID, runtimeName) {
			slog.Error("Failed to persist runtime interface name")
			return httputil.NewAppError(http.StatusInternalServerError, "failed to persist runtime interface name")
		}
	}

	return nil
}

func (s *Service) syncRuntimeInterfaceAddresses(ctx context.Context, nodeID string) error {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	for _, iface := range node.Interfaces {
		if iface.IPAddr == "" || iface.PrefixLen == 0 || iface.RuntimeName == "" {
			continue
		}
		if err := s.applyRuntimeInterfaceAddress(ctx, node, iface); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) syncRuntimeRoutes(ctx context.Context, nodeID string) error {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	for _, route := range node.Routes {
		if err := s.applyRuntimeRoute(ctx, node, route); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) stopNode(ctx context.Context, nodeID string) error {
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
		slog.Error("Container inspect failed", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State != nil && !inspect.State.Running {
		s.repo.UpdateNodeStatus(nodeID, model.Idle)
		return nil
	}

	if err := s.docker.ContainerStop(ctx, node.ContainerID, container.StopOptions{}); err != nil {
		slog.Error("Failed to stop node", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "failed to stop node: "+err.Error())
	}

	s.repo.UpdateNodeStatus(nodeID, model.Idle)
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

func (s *Service) execCommand(ctx context.Context, containerID string, execCmd []string, command string) (commandResponse, error) {
	stdout, stderr, exitCode, err := execInContainer(ctx, s.docker, containerID, execCmd)
	if err != nil {
		return commandResponse{}, err
	}

	return commandResponse{
		Command:  command,
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
	}, nil
}

func (s *Service) runCommand(ctx context.Context, nodeID, command string) (commandResponse, error) {
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
		slog.Error("Container inspect failed", "err", err)
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State == nil || !inspect.State.Running {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "node is not running")
	}

	if command == "ip addr" {
		return s.runIPAddr(command, node), nil
	}
	if command == "help" {
		return runHelp(command, node.Type), nil
	}

	fields := strings.Fields(command)
	if node.Type == model.Switch {
		if len(fields) >= 2 && fields[0] == "ping" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support ping")
		}
		if len(fields) >= 2 && fields[0] == "traceroute" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support traceroute")
		}
		if len(fields) >= 1 && fields[0] == "iperf" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support iperf")
		}
		if len(fields) >= 2 && fields[0] == "ip" && fields[1] == "set" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch ports do not support ip assignment")
		}
		if len(fields) >= 2 && fields[0] == "ip" && fields[1] == "route" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support routing commands")
		}
	}
	if len(fields) == 2 && fields[0] == "ping" {
		return s.runPing(ctx, command, node)
	}
	if len(fields) == 2 && fields[0] == "traceroute" {
		return s.runTraceroute(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "iperf" && fields[1] == "tcp" {
		return s.runIperfTCP(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "iperf" && fields[1] == "udp" {
		return s.runIperfUDP(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "iperf" && fields[1] == "server" && fields[2] == "start" {
		return s.runIperfServerStart(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "iperf" && fields[1] == "server" && fields[2] == "stop" {
		return s.runIperfServerStop(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "iperf" && fields[1] == "server" && fields[2] == "status" {
		return s.runIperfServerStatus(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "iperf" && fields[1] == "server" && fields[2] == "log" {
		return s.runIperfServerLog(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "iperf" && fields[1] == "server" && fields[2] == "logclear" {
		return s.runIperfServerLogClear(ctx, command, node)
	}
	if len(fields) == 2 && fields[0] == "ip" && fields[1] == "route" {
		return runIPRouteList(command, node), nil
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "set" {
		return s.runIPSet(ctx, command, nodeID, fields[2], fields[3])
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "route" && fields[2] == "default" {
		return s.runIPRoute(ctx, command, node, "0.0.0.0/0", fields[3])
	}
	if len(fields) == 6 && fields[0] == "ip" && fields[1] == "route" && fields[2] == "add" && fields[4] == "via" {
		return s.runIPRoute(ctx, command, node, fields[3], fields[5])
	}

	return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "unsupported command: "+command)
}

func runHelp(command string, nodeType model.NodeType) commandResponse {
	lines := []string{
		"help",
		"clear",
	}

	if nodeType != model.Switch {
		lines = append(lines,
			"ip addr",
			"ip set [interface] [ip/prefix]",
			"ip route",
			"ip route default [next-hop]",
			"ip route add [destination/prefix] via [next-hop]",
			"ping [target-ip]",
			"traceroute [target-ip]",
			"iperf tcp [ip]",
			"iperf udp [ip]",
			"iperf server start",
			"iperf server stop",
			"iperf server status",
			"iperf server log",
			"iperf server logclear",
		)
	}

	return commandResponse{
		Command:  command,
		Stdout:   strings.Join(lines, "\n"),
		Stderr:   "",
		ExitCode: 0,
	}
}

func (s *Service) runIPAddr(command string, node model.Node) commandResponse {
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

func (s *Service) runPing(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetLogicalIP := fields[1]
	if _, err := netip.ParseAddr(targetLogicalIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}

	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"ping", "-c", "4", targetLogicalIP},
		command,
	)
}

func (s *Service) runTraceroute(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetLogicalIP := fields[1]
	if _, err := netip.ParseAddr(targetLogicalIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}

	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"traceroute", "-n", "-w", "1", "-q", "1", targetLogicalIP},
		command,
	)
}

func (s *Service) runIperfTCP(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetIP := fields[2]
	if _, err := netip.ParseAddr(targetIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}

	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"iperf3", "-c", targetIP, "-t", "5"},
		command,
	)
}

func (s *Service) runIperfUDP(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetIP := fields[2]
	if _, err := netip.ParseAddr(targetIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}

	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"iperf3", "-u", "-c", targetIP, "-t", "5"},
		command,
	)
}

func (s *Service) runIperfServerStart(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	running, err := s.iperfServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	if running {
		return commandResponse{
			Command:  command,
			Stdout:   "iperf server already running",
			Stderr:   "",
			ExitCode: 0,
		}, nil
	}

	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"sh", "-c", "mkdir -p /var/log/iperf && nohup iperf3 -s >" + iperfLogPath + " 2>&1 &"},
		"failed to start iperf server",
	); err != nil {
		return commandResponse{}, err
	}

	return commandResponse{
		Command:  command,
		Stdout:   "iperf server started",
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) runIperfServerStop(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", "pkill -x iperf3 >/dev/null 2>&1 || true"},
		command,
	)
}

func (s *Service) runIperfServerStatus(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", `pgrep -x iperf3 >/dev/null && echo "running" || echo "stopped"`},
		command,
	)
}

func (s *Service) runIperfServerLog(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", "cat " + iperfLogPath + " 2>/dev/null || true"},
		command,
	)
}

func (s *Service) runIperfServerLogClear(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", "mkdir -p /var/log/iperf && : >" + iperfLogPath},
		command,
	)
}

func (s *Service) iperfServerRunning(ctx context.Context, containerID string) (bool, error) {
	stdout, stderr, exitCode, err := execInContainer(
		ctx,
		s.docker,
		containerID,
		[]string{"sh", "-c", "pgrep -x iperf3 >/dev/null && echo true || echo false"},
	)
	if err != nil {
		return false, err
	}
	if exitCode != 0 {
		message := "failed to inspect iperf server status"
		if trimmed := strings.TrimSpace(stderr); trimmed != "" {
			message += ": " + trimmed
		}
		slog.Error("Container exec failed", "message", message)
		return false, httputil.NewAppError(http.StatusInternalServerError, message)
	}

	return strings.TrimSpace(stdout) == "true", nil
}

func (s *Service) runIPSet(ctx context.Context, command, nodeID, interfaceName, cidr string) (commandResponse, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid interface address")
	}

	if !s.repo.UpdateInterfaceAddress(nodeID, interfaceName, prefix.Addr().String(), prefix.Bits()) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
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
		slog.Error("Container inspect failed", "err", err)
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State != nil && inspect.State.Running {
		refreshedNode, ok := s.repo.GetNode(nodeID)
		if !ok {
			return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
		}

		foundInterface := false
		targetIface := model.Interface{}
		for _, iface := range refreshedNode.Interfaces {
			if iface.Name != interfaceName {
				continue
			}
			foundInterface = true
			targetIface = iface
			break
		}
		if !foundInterface {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
		}
		if targetIface.LinkID == "" {
			return commandResponse{
				Command:  command,
				Stdout:   fmt.Sprintf("%s set to %s", interfaceName, cidr),
				Stderr:   "",
				ExitCode: 0,
			}, nil
		}

		if targetIface.RuntimeName == "" {
			s.syncRuntimeInterfaces(nodeID, refreshedNode.Interfaces, inspect)
			if err := s.syncRuntimeInterfaceNames(ctx, nodeID); err != nil {
				return commandResponse{}, err
			}

			refreshedNode, ok = s.repo.GetNode(nodeID)
			if !ok {
				return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
			}

			targetIface = model.Interface{}
			for _, iface := range refreshedNode.Interfaces {
				if iface.Name != interfaceName {
					continue
				}
				targetIface = iface
				break
			}
			if targetIface.RuntimeName == "" {
				slog.Error("Runtime interface name resolution failed")
				return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "runtime interface name resolution failed")
			}
		}

		if err := s.applyRuntimeInterfaceAddress(ctx, refreshedNode, targetIface); err != nil {
			return commandResponse{}, err
		}
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("%s set to %s", interfaceName, cidr),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) applyRuntimeInterfaceAddress(ctx context.Context, node model.Node, iface model.Interface) error {
	if iface.RuntimeName == "" {
		return httputil.NewAppError(http.StatusBadRequest, "runtime interface name not resolved")
	}
	if iface.IPAddr == "" || iface.PrefixLen == 0 {
		return nil
	}

	cidr := fmt.Sprintf("%s/%d", iface.IPAddr, iface.PrefixLen)
	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "addr", "replace", cidr, "dev", iface.RuntimeName},
		"failed to apply runtime interface address",
	); err != nil {
		return err
	}

	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "link", "set", iface.RuntimeName, "up"},
		"failed to bring runtime interface up",
	); err != nil {
		return err
	}

	return nil
}

func runIPRouteList(command string, node model.Node) commandResponse {
	lines := make([]string, 0, len(node.Routes))
	for _, route := range node.Routes {
		if route.Destination == "0.0.0.0/0" {
			lines = append(lines, fmt.Sprintf("default via %s", route.NextHop))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s via %s", route.Destination, route.NextHop))
	}

	return commandResponse{
		Command:  command,
		Stdout:   strings.Join(lines, "\n"),
		Stderr:   "",
		ExitCode: 0,
	}
}

func (s *Service) runIPRoute(
	ctx context.Context,
	command string,
	node model.Node,
	destination string,
	nextHop string,
) (commandResponse, error) {
	if destination != "0.0.0.0/0" {
		prefix, err := netip.ParsePrefix(destination)
		if err != nil {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid route destination")
		}
		destination = prefix.Masked().String()
	}

	if _, err := netip.ParseAddr(nextHop); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid next hop")
	}

	route := model.Route{
		Destination: destination,
		NextHop:     nextHop,
	}

	if err := s.applyRuntimeRoute(ctx, node, route); err != nil {
		return commandResponse{}, err
	}

	if !s.repo.UpsertRoute(node.ID, route) {
		slog.Error("Failed to persist route")
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "failed to persist route")
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("route %s via %s configured", route.Destination, route.NextHop),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) applyRuntimeRoute(ctx context.Context, node model.Node, route model.Route) error {
	s.repo.store.Mu.RLock()
	defer s.repo.store.Mu.RUnlock()

	_, _, ok := s.findReachableNextHopLocked(node, route.NextHop)
	if !ok {
		return httputil.NewAppError(http.StatusBadRequest, "next hop is not directly reachable")
	}

	destination := route.Destination
	if destination == "0.0.0.0/0" {
		destination = "default"
	}

	execCmd := []string{"ip", "route", "replace", destination, "via", route.NextHop}

	if _, err := execInContainerChecked(ctx, s.docker, node.ContainerID, execCmd, "failed to apply runtime route"); err != nil {
		return err
	}

	return nil
}

func (s *Service) findReachableNextHopLocked(node model.Node, nextHop string) (model.Node, model.Interface, bool) {
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

		peerNode, peerIface, ok := s.findInterfaceThroughSwitchesLocked(sourceIface.ID, map[string]struct{}{}, func(candidateNode model.Node, candidateIface model.Interface) bool {
			return candidateNode.Type != model.Switch && candidateIface.IPAddr == nextHop
		})
		if ok {
			return peerNode, peerIface, true
		}
	}

	return model.Node{}, model.Interface{}, false
}

func (s *Service) findInterfaceThroughSwitchesLocked(
	interfaceID string,
	visited map[string]struct{},
	match func(model.Node, model.Interface) bool,
) (model.Node, model.Interface, bool) {
	if _, ok := visited[interfaceID]; ok {
		return model.Node{}, model.Interface{}, false
	}
	visited[interfaceID] = struct{}{}

	_, iface, ok := s.findInterfaceOwnerLocked(interfaceID)
	if !ok || iface.LinkID == "" {
		return model.Node{}, model.Interface{}, false
	}

	link, ok := s.repo.store.Links[iface.LinkID]
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

	peerNode, peerIface, ok := s.findInterfaceOwnerLocked(peerInterfaceID)
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
		foundNode, foundIface, found := s.findInterfaceThroughSwitchesLocked(switchIface.ID, visited, match)
		if found {
			return foundNode, foundIface, true
		}
	}

	return model.Node{}, model.Interface{}, false
}

func (s *Service) findInterfaceOwnerLocked(interfaceID string) (model.Node, model.Interface, bool) {
	nodeID, ok := s.repo.store.InterfaceOwnerIndex[interfaceID]
	if !ok {
		return model.Node{}, model.Interface{}, false
	}

	node, ok := s.repo.store.Nodes[nodeID]
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

func (s *Service) findBestRouteLocked(node model.Node, targetAddr netip.Addr) (model.Route, bool) {
	var best model.Route
	bestBits := -1

	for _, route := range node.Routes {
		if route.Destination == "0.0.0.0/0" {
			if bestBits < 0 {
				best = route
				bestBits = 0
			}
			continue
		}

		prefix, err := netip.ParsePrefix(route.Destination)
		if err != nil {
			continue
		}
		prefix = prefix.Masked()
		if !prefix.Contains(targetAddr) {
			continue
		}
		if prefix.Bits() <= bestBits {
			continue
		}

		best = route
		bestBits = prefix.Bits()
	}

	if bestBits < 0 {
		return model.Route{}, false
	}

	return best, true
}
