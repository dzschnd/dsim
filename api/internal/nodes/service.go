package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"strconv"
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

const (
	iperfLogPath     = "/var/log/iperf/iperf.log"
	httpLogPath      = "/var/log/http/http.log"
	httpPIDFilePath  = "/var/run/http-server.pid"
	httpPortFilePath = "/var/run/http-server.port"
	tcpPIDFilePath   = "/var/run/tcp-server.pid"
	udpPIDFilePath   = "/var/run/udp-server.pid"

	minIperfUDPPacketLength = 16
	maxIperfUDPPacketLength = 65507

	httpDefaultPort = 8080
	tcpDefaultPort  = 3000
	udpDefaultPort  = 4000
)

var iperfBitratePattern = regexp.MustCompile(`^[1-9][0-9]*(\.[0-9]+)?[KMGkmg]?$`)
var iperfErrorPrefixPattern = regexp.MustCompile(`(^|\n)iperf3:\s*`)
var curlErrorPrefixPattern = regexp.MustCompile(`(^|\n)curl: \([0-9]+\)\s*`)

type listenerKind string

const (
	listenerIperf listenerKind = "iperf"
	listenerHTTP  listenerKind = "http"
	listenerTCP   listenerKind = "tcp"
	listenerUDP   listenerKind = "udp"
)

func NewService(docker *client.Client, s *store.Store) *Service {
	repo := newRepository(s)
	linkRepo := links.NewRepository(s)
	return &Service{docker: docker, repo: repo, linkRepo: linkRepo}
}

func (s *Service) getNodes() ([]model.Node, error) {
	return s.repo.ListNodes(), nil
}

func (s *Service) getNode(nodeID string) (model.Node, error) {
	if strings.TrimSpace(nodeID) == "" {
		return model.Node{}, httputil.NewAppError(http.StatusBadRequest, "node id required")
	}

	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return model.Node{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	return node, nil
}

// TODO: add error handling for invalid type
func nodeTypeTag(t model.NodeType) (string, int) {
	var image string
	var ifaceCount int
	image = strings.TrimSpace(os.Getenv("NODE_IMAGE"))
	if image == "" {
		image = "dsim/node:local"
	}
	switch t {
	case model.Host:
		ifaceCount = 1
	case model.Switch:
		ifaceCount = 8
	case model.Router:
		ifaceCount = 4
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

	node := model.Node{
		ID:          nodeID,
		Name:        "",
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
		if err := s.syncRuntimeInterfaceConditions(ctx, nodeID); err != nil {
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
	if err := s.syncRuntimeInterfaceConditions(ctx, nodeID); err != nil {
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

func (s *Service) syncRuntimeInterfaceConditions(ctx context.Context, nodeID string) error {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	for _, iface := range node.Interfaces {
		if err := s.applyRuntimeInterfaceConditions(ctx, node, iface); err != nil {
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
		if timeoutErr := execContextError(ctx, err); timeoutErr != nil {
			return "", "", 0, timeoutErr
		}
		slog.Error("Exec create failed", "err", err)
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec create failed")
	}

	attachResp, err := docker.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		if timeoutErr := execContextError(ctx, err); timeoutErr != nil {
			return "", "", 0, timeoutErr
		}
		slog.Error("Exec attach failed", "err", err)
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec attach failed")
	}
	defer attachResp.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		if timeoutErr := execContextError(ctx, err); timeoutErr != nil {
			return "", "", 0, timeoutErr
		}
		slog.Error("Exec read failed", "err", err)
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec read failed")
	}

	execInspect, err := docker.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		if timeoutErr := execContextError(ctx, err); timeoutErr != nil {
			return "", "", 0, timeoutErr
		}
		slog.Error("Exec inspect failed", "err", err)
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec inspect failed")
	}

	return stdout.String(), stderr.String(), execInspect.ExitCode, nil
}

func execContextError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return httputil.NewAppError(http.StatusRequestTimeout, "command timed out")
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return httputil.NewAppError(http.StatusRequestTimeout, "command canceled")
	}
	return nil
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
		if len(fields) >= 1 && fields[0] == "http" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support http")
		}
		if len(fields) >= 1 && fields[0] == "tcp" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support tcp")
		}
		if len(fields) >= 1 && fields[0] == "udp" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support udp")
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
	if len(fields) == 4 && fields[0] == "traceroute" && fields[2] == "--max-hops" {
		return s.runTraceroute(ctx, command, node)
	}
	if len(fields) >= 3 && fields[0] == "iperf" && fields[1] == "tcp" {
		return s.runIperfTCP(ctx, command, node)
	}
	if len(fields) >= 3 && fields[0] == "iperf" && fields[1] == "udp" {
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
	if len(fields) == 4 && fields[0] == "iperf" && fields[1] == "server" && fields[2] == "log" && fields[3] == "clear" {
		return s.runIperfServerLogClear(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "http" && fields[1] == "get" {
		return s.runHTTPGet(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "http" && fields[1] == "server" && fields[2] == "start" {
		return s.runHTTPServerStart(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "http" && fields[1] == "server" && fields[2] == "stop" {
		return s.runHTTPServerStop(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "http" && fields[1] == "server" && fields[2] == "status" {
		return s.runHTTPServerStatus(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "http" && fields[1] == "server" && fields[2] == "log" {
		return s.runHTTPServerLog(ctx, command, node)
	}
	if len(fields) == 4 && fields[0] == "http" && fields[1] == "server" && fields[2] == "log" && fields[3] == "clear" {
		return s.runHTTPServerLogClear(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "tcp" && fields[1] == "server" && fields[2] == "start" {
		return s.runTCPServerStart(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "tcp" && fields[1] == "server" && fields[2] == "stop" {
		return s.runTCPServerStop(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "tcp" && fields[1] == "server" && fields[2] == "status" {
		return s.runTCPServerStatus(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "tcp" && fields[1] == "connect" {
		return s.runTCPConnect(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "udp" && fields[1] == "server" && fields[2] == "start" {
		return s.runUDPServerStart(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "udp" && fields[1] == "server" && fields[2] == "stop" {
		return s.runUDPServerStop(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "udp" && fields[1] == "server" && fields[2] == "status" {
		return s.runUDPServerStatus(ctx, command, node)
	}
	if len(fields) == 3 && fields[0] == "udp" && fields[1] == "probe" {
		return s.runUDPProbe(ctx, command, node)
	}
	if len(fields) >= 3 && fields[0] == "tc" && fields[1] == "set" {
		return s.runTCSet(ctx, command, nodeID, fields[2], fields[3:])
	}
	if len(fields) == 3 && fields[0] == "tc" && fields[1] == "clear" {
		return s.runTCClear(ctx, command, nodeID, fields[2])
	}
	if len(fields) == 3 && fields[0] == "tc" && fields[1] == "show" {
		return s.runTCShow(command, node, fields[2])
	}
	if len(fields) == 2 && fields[0] == "ip" && fields[1] == "route" {
		return runIPRouteList(command, node), nil
	}
	if len(fields) == 3 && fields[0] == "ip" && fields[1] == "unset" {
		return s.runIPUnset(ctx, command, nodeID, fields[2])
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "set" {
		return s.runIPSet(ctx, command, nodeID, fields[2], fields[3])
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "route" && fields[2] == "delete" {
		return s.runIPRouteDelete(ctx, command, node, fields[3])
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "route" && fields[2] == "default" {
		return s.runIPRoute(ctx, command, node, "0.0.0.0/0", fields[3])
	}
	if len(fields) == 6 && fields[0] == "ip" && fields[1] == "route" && fields[2] == "add" && fields[4] == "via" {
		return s.runIPRoute(ctx, command, node, fields[3], fields[5])
	}

	if usage, ok := commandUsage(fields, node.Type); ok {
		return commandResponse{
			Command:  command,
			Stdout:   usage,
			Stderr:   "",
			ExitCode: 2,
		}, nil
	}

	return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "unsupported command: "+command)
}

func commandUsage(fields []string, nodeType model.NodeType) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}

	switch fields[0] {
	case "ip":
		if nodeType == model.Switch {
			return "switch does not support ip commands", true
		}
		return ipCommandUsage(fields)
	case "tc":
		return tcCommandUsage(fields)
	case "iperf":
		if nodeType == model.Switch {
			return "switch does not support iperf", true
		}
		return iperfCommandUsage(fields), true
	case "http":
		if nodeType == model.Switch {
			return "switch does not support http", true
		}
		return httpCommandUsage(fields), true
	case "tcp":
		if nodeType == model.Switch {
			return "switch does not support tcp", true
		}
		return tcpCommandUsage(fields), true
	case "udp":
		if nodeType == model.Switch {
			return "switch does not support udp", true
		}
		return udpCommandUsage(fields), true
	case "ping":
		if nodeType == model.Switch {
			return "switch does not support ping", true
		}
		return "ping [target-ip]", true
	case "traceroute":
		if nodeType == model.Switch {
			return "switch does not support traceroute", true
		}
		return "traceroute [target-ip] [--max-hops count(1..255)]", true
	}

	return "", false
}

func ipCommandUsage(fields []string) (string, bool) {
	ipCommands := []string{
		"ip addr",
		"ip set [interface] [ip/prefix]",
		"ip unset [interface]",
		"ip route",
		"ip route default [next-hop]",
		"ip route add [destination/prefix] via [next-hop]",
		"ip route delete [default|destination/prefix]",
	}
	ipRouteCommands := []string{
		"ip route",
		"ip route default [next-hop]",
		"ip route add [destination/prefix] via [next-hop]",
		"ip route delete [default|destination/prefix]",
	}

	if len(fields) == 1 {
		return strings.Join(ipCommands, "\n"), true
	}

	switch fields[1] {
	case "addr":
		return "ip addr", true
	case "set":
		return "ip set [interface] [ip/prefix]", true
	case "unset":
		return "ip unset [interface]", true
	case "route":
		if len(fields) == 2 {
			return "", false
		}
		switch fields[2] {
		case "default":
			return "ip route default [next-hop]", true
		case "add":
			return "ip route add [destination/prefix] via [next-hop]", true
		case "delete":
			return "ip route delete [default|destination/prefix]", true
		default:
			return strings.Join(ipRouteCommands, "\n"), true
		}
	default:
		return strings.Join(ipCommands, "\n"), true
	}
}

func tcCommandUsage(fields []string) (string, bool) {
	tcCommands := []string{
		"tc show [interface]",
		"tc clear [interface]",
		"tc set [interface] [--delay ms] [--jitter ms] [--loss pct] [--bandwidth kbit]",
	}

	if len(fields) == 1 {
		return strings.Join(tcCommands, "\n"), true
	}

	switch fields[1] {
	case "show":
		return "tc show [interface]", true
	case "clear":
		return "tc clear [interface]", true
	case "set":
		return "tc set [interface] [--delay ms] [--jitter ms] [--loss pct] [--bandwidth kbit]", true
	default:
		return strings.Join(tcCommands, "\n"), true
	}
}

func iperfCommandUsage(fields []string) string {
	iperfCommands := []string{
		"iperf tcp [ip] [--time seconds | --bytes bytes]",
		"iperf udp [ip] [--time seconds | --bytes bytes] [--bitrate rate[K|M|G]] [--packet-length bytes(16..65507)]",
		"iperf server start",
		"iperf server stop",
		"iperf server status",
		"iperf server log",
		"iperf server log clear",
	}
	iperfServerCommands := []string{
		"iperf server start",
		"iperf server stop",
		"iperf server status",
		"iperf server log",
		"iperf server log clear",
	}

	if len(fields) == 1 {
		return strings.Join(iperfCommands, "\n")
	}

	switch fields[1] {
	case "tcp":
		return "iperf tcp [ip] [--time seconds | --bytes bytes]"
	case "udp":
		return "iperf udp [ip] [--time seconds | --bytes bytes] [--bitrate rate[K|M|G]] [--packet-length bytes(16..65507)]"
	case "server":
		if len(fields) == 2 {
			return strings.Join(iperfServerCommands, "\n")
		}
		switch fields[2] {
		case "start":
			return "iperf server start"
		case "stop":
			return "iperf server stop"
		case "status":
			return "iperf server status"
		case "log":
			if len(fields) == 3 {
				return "iperf server log"
			}
			if fields[3] == "clear" {
				return "iperf server log clear"
			}
			return "iperf server log clear"
		default:
			return strings.Join(iperfServerCommands, "\n")
		}
	default:
		return strings.Join(iperfCommands, "\n")
	}
}

func httpCommandUsage(fields []string) string {
	httpCommands := []string{
		"http get [ip]",
		"http server start",
		"http server stop",
		"http server status",
		"http server log",
		"http server log clear",
	}
	httpServerCommands := []string{
		"http server start",
		"http server stop",
		"http server status",
		"http server log",
		"http server log clear",
	}

	if len(fields) == 1 {
		return strings.Join(httpCommands, "\n")
	}

	switch fields[1] {
	case "get":
		return "http get [ip]"
	case "server":
		if len(fields) == 2 {
			return strings.Join(httpServerCommands, "\n")
		}
		switch fields[2] {
		case "start":
			return "http server start"
		case "stop":
			return "http server stop"
		case "status":
			return "http server status"
		case "log":
			if len(fields) == 3 {
				return "http server log"
			}
			if fields[3] == "clear" {
				return "http server log clear"
			}
			return "http server log clear"
		default:
			return strings.Join(httpServerCommands, "\n")
		}
	default:
		return strings.Join(httpCommands, "\n")
	}
}

func tcpCommandUsage(fields []string) string {
	tcpCommands := []string{
		"tcp server start",
		"tcp server stop",
		"tcp server status",
		"tcp connect [ip]",
	}
	tcpServerCommands := []string{
		"tcp server start",
		"tcp server stop",
		"tcp server status",
	}

	if len(fields) == 1 {
		return strings.Join(tcpCommands, "\n")
	}

	switch fields[1] {
	case "connect":
		return "tcp connect [ip]"
	case "server":
		if len(fields) == 2 {
			return strings.Join(tcpServerCommands, "\n")
		}
		switch fields[2] {
		case "start":
			return "tcp server start"
		case "stop":
			return "tcp server stop"
		case "status":
			return "tcp server status"
		default:
			return strings.Join(tcpServerCommands, "\n")
		}
	default:
		return strings.Join(tcpCommands, "\n")
	}
}

func udpCommandUsage(fields []string) string {
	udpCommands := []string{
		"udp server start",
		"udp server stop",
		"udp server status",
		"udp probe [ip]",
	}
	udpServerCommands := []string{
		"udp server start",
		"udp server stop",
		"udp server status",
	}

	if len(fields) == 1 {
		return strings.Join(udpCommands, "\n")
	}

	switch fields[1] {
	case "probe":
		return "udp probe [ip]"
	case "server":
		if len(fields) == 2 {
			return strings.Join(udpServerCommands, "\n")
		}
		switch fields[2] {
		case "start":
			return "udp server start"
		case "stop":
			return "udp server stop"
		case "status":
			return "udp server status"
		default:
			return strings.Join(udpServerCommands, "\n")
		}
	default:
		return strings.Join(udpCommands, "\n")
	}
}

func runHelp(command string, nodeType model.NodeType) commandResponse {
	lines := []string{
		"help",
		"clear",
		"history",
	}

	if nodeType != model.Switch {
		lines = append(lines,
			"ip addr",
			"ip set [interface] [ip/prefix]",
			"ip unset [interface]",
			"ip route",
			"ip route default [next-hop]",
			"ip route add [destination/prefix] via [next-hop]",
			"ip route delete [default|destination/prefix]",
			"ping [target-ip]",
			"traceroute [target-ip] [--max-hops count(1..255)]",
			"iperf tcp [ip] [--time seconds | --bytes bytes]",
			"iperf udp [ip] [--time seconds | --bytes bytes] [--bitrate rate[K|M|G]] [--packet-length bytes(16..65507)]",
			"iperf server start",
			"iperf server stop",
			"iperf server status",
			"iperf server log",
			"iperf server log clear",
			"http get [ip]",
			"http server start",
			"http server stop",
			"http server status",
			"http server log",
			"http server log clear",
			"tcp server start",
			"tcp server stop",
			"tcp server status",
			"tcp connect [ip]",
			"udp server start",
			"udp server stop",
			"udp server status",
			"udp probe [ip]",
		)
	}
	lines = append(lines,
		"tc show [interface]",
		"tc clear [interface]",
		"tc set [interface] [--delay ms] [--jitter ms] [--loss pct] [--bandwidth kbit]",
	)

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
		[]string{"ping", "-c", "2", targetLogicalIP},
		command,
	)
}

func (s *Service) runTraceroute(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetLogicalIP := fields[1]
	if _, err := netip.ParseAddr(targetLogicalIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}
	maxHops := 30
	if len(fields) != 2 {
		if len(fields) != 4 || fields[2] != "--max-hops" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid traceroute syntax")
		}
		parsedMaxHops, err := strconv.Atoi(fields[3])
		if err != nil {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "max hops must be an integer")
		}
		if parsedMaxHops < 1 || parsedMaxHops > 255 {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "max hops must be between 1 and 255")
		}
		maxHops = parsedMaxHops
	}

	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"traceroute", "-n", "-w", "1", "-q", "1", "-m", strconv.Itoa(maxHops), targetLogicalIP},
		command,
	)
}

func (s *Service) runIperfTCP(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetIP := fields[2]
	if _, err := netip.ParseAddr(targetIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}
	iperfArgs, err := parseIperfClientArgs(fields[3:], false)
	if err != nil {
		return commandResponse{}, err
	}

	response, err := s.execCommand(
		ctx,
		node.ContainerID,
		append([]string{"iperf3", "-c", targetIP}, iperfArgs...),
		command,
	)
	if err != nil {
		return commandResponse{}, err
	}
	response.Stderr = sanitizeIperfError(response.Stderr)
	return response, nil
}

func (s *Service) runIperfUDP(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetIP := fields[2]
	if _, err := netip.ParseAddr(targetIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}
	iperfArgs, err := parseIperfClientArgs(fields[3:], true)
	if err != nil {
		return commandResponse{}, err
	}

	response, err := s.execCommand(
		ctx,
		node.ContainerID,
		append([]string{"iperf3", "-u", "-c", targetIP}, iperfArgs...),
		command,
	)
	if err != nil {
		return commandResponse{}, err
	}
	response.Stderr = sanitizeIperfError(response.Stderr)
	return response, nil
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

	if err := s.ensureListenerAvailable(ctx, node.ContainerID, listenerIperf, 5201); err != nil {
		return commandResponse{}, err
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

	running, err = s.iperfServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	if !running {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "iperf server failed to start")
	}

	portBusy, err := s.portBusy(ctx, node.ContainerID, 5201, "tcp")
	if err != nil {
		return commandResponse{}, err
	}
	if !portBusy {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "iperf server failed to bind port 5201")
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

func (s *Service) runHTTPGet(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetIP := fields[2]
	if _, err := netip.ParseAddr(targetIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}
	targetURL := fmt.Sprintf("http://%s:%d", targetIP, httpDefaultPort)

	response, err := s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"curl", "-sS", "-i", "--connect-timeout", "2", "--max-time", "5", targetURL},
		command,
	)
	if err != nil {
		return commandResponse{}, err
	}
	response.Stderr = sanitizeCurlError(response.Stderr)
	return response, nil
}

func (s *Service) runHTTPServerStart(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	port := httpDefaultPort

	running, err := s.httpServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	if running {
		return commandResponse{
			Command:  command,
			Stdout:   "http server already running",
			Stderr:   "",
			ExitCode: 0,
		}, nil
	}

	if err := s.ensureListenerAvailable(ctx, node.ContainerID, listenerHTTP, port); err != nil {
		return commandResponse{}, err
	}

	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"sh", "-c", fmt.Sprintf("mkdir -p /srv/http /var/log/http /var/run && nohup darkhttpd /srv/http --port %d >%s 2>&1 & echo $! >%s && echo %d >%s", port, httpLogPath, httpPIDFilePath, port, httpPortFilePath)},
		"failed to start http server",
	); err != nil {
		return commandResponse{}, err
	}

	running, err = s.httpServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	if !running {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "http server failed to start")
	}

	portBusy, err := s.portBusy(ctx, node.ContainerID, port, "tcp")
	if err != nil {
		return commandResponse{}, err
	}
	if !portBusy {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, fmt.Sprintf("http server failed to bind port %d", port))
	}

	return commandResponse{
		Command:  command,
		Stdout:   "http server started",
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) runHTTPServerStop(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", "pkill -x darkhttpd >/dev/null 2>&1 || true; rm -f " + httpPIDFilePath + " " + httpPortFilePath},
		command,
	)
}

func (s *Service) runHTTPServerStatus(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", `pgrep -x darkhttpd >/dev/null && echo "running" || echo "stopped"`},
		command,
	)
}

func (s *Service) runHTTPServerLog(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", "cat " + httpLogPath + " 2>/dev/null || true"},
		command,
	)
}

func (s *Service) runHTTPServerLogClear(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", "mkdir -p /var/log/http && : >" + httpLogPath},
		command,
	)
}

func (s *Service) httpServerRunning(ctx context.Context, containerID string) (bool, error) {
	return s.execBoolCommand(ctx, containerID, "pgrep -x darkhttpd >/dev/null && echo true || echo false", "failed to inspect http server status")
}

func (s *Service) runTCPServerStart(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	port := tcpDefaultPort

	running, err := s.tcpServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	if running {
		return commandResponse{
			Command:  command,
			Stdout:   "tcp server already running",
			Stderr:   "",
			ExitCode: 0,
		}, nil
	}

	if err := s.ensureListenerAvailable(ctx, node.ContainerID, listenerTCP, port); err != nil {
		return commandResponse{}, err
	}

	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"sh", "-c", fmt.Sprintf("mkdir -p /var/run && nohup sh -c 'while true; do nc -l -p %d >/dev/null 2>&1; done' >/dev/null 2>&1 & echo $! >%s", port, tcpPIDFilePath)},
		"failed to start tcp server",
	); err != nil {
		return commandResponse{}, err
	}

	running, err = s.tcpServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	if !running {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "tcp server failed to start")
	}

	portBusy, err := s.portBusy(ctx, node.ContainerID, port, "tcp")
	if err != nil {
		return commandResponse{}, err
	}
	if !portBusy {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, fmt.Sprintf("tcp server failed to bind port %d", port))
	}

	return commandResponse{
		Command:  command,
		Stdout:   "tcp server started",
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) runTCPServerStop(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", fmt.Sprintf("if [ -f %s ]; then pid=$(cat %s); pkill -P \"$pid\" >/dev/null 2>&1 || true; kill \"$pid\" >/dev/null 2>&1 || true; fi; rm -f %s", tcpPIDFilePath, tcpPIDFilePath, tcpPIDFilePath)},
		command,
	)
}

func (s *Service) runTCPServerStatus(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	running, err := s.tcpServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	status := "stopped"
	if running {
		status = "running"
	}

	return commandResponse{Command: command, Stdout: status, Stderr: "", ExitCode: 0}, nil
}

func (s *Service) runTCPConnect(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetIP := fields[2]
	if _, err := netip.ParseAddr(targetIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}
	port := tcpDefaultPort

	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", fmt.Sprintf("if nc -vz -w 5 %s %d >/dev/null 2>&1; then echo 'tcp connect succeeded'; exit 0; fi; echo 'tcp connect failed'; exit 1", targetIP, port)},
		command,
	)
}

func (s *Service) runUDPServerStart(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	port := udpDefaultPort

	running, err := s.udpServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	if running {
		return commandResponse{
			Command:  command,
			Stdout:   "udp server already running",
			Stderr:   "",
			ExitCode: 0,
		}, nil
	}

	if err := s.ensureListenerAvailable(ctx, node.ContainerID, listenerUDP, port); err != nil {
		return commandResponse{}, err
	}

	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"sh", "-c", fmt.Sprintf(`mkdir -p /var/run && nohup socat -T5 UDP-RECVFROM:%d,fork SYSTEM:"printf ack" >/dev/null 2>&1 & echo $! >%s`, port, udpPIDFilePath)},
		"failed to start udp server",
	); err != nil {
		return commandResponse{}, err
	}

	running, err = s.udpServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	if !running {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "udp server failed to start")
	}

	portBusy, err := s.portBusy(ctx, node.ContainerID, port, "udp")
	if err != nil {
		return commandResponse{}, err
	}
	if !portBusy {
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, fmt.Sprintf("udp server failed to bind port %d", port))
	}

	return commandResponse{
		Command:  command,
		Stdout:   "udp server started",
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) runUDPServerStop(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", fmt.Sprintf("if [ -f %s ]; then kill \"$(cat %s)\" >/dev/null 2>&1 || true; fi; rm -f %s", udpPIDFilePath, udpPIDFilePath, udpPIDFilePath)},
		command,
	)
}

func (s *Service) runUDPServerStatus(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	running, err := s.udpServerRunning(ctx, node.ContainerID)
	if err != nil {
		return commandResponse{}, err
	}
	status := "stopped"
	if running {
		status = "running"
	}

	return commandResponse{Command: command, Stdout: status, Stderr: "", ExitCode: 0}, nil
}

func (s *Service) runUDPProbe(ctx context.Context, command string, node model.Node) (commandResponse, error) {
	fields := strings.Fields(command)
	targetIP := fields[2]
	if _, err := netip.ParseAddr(targetIP); err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid target ip")
	}
	port := udpDefaultPort

	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"sh", "-c", fmt.Sprintf(`reply=$(printf probe | socat -T5 - UDP:%s:%d 2>/dev/null); if [ "$reply" = "ack" ]; then echo "udp probe succeeded"; else echo "udp probe failed"; exit 1; fi`, targetIP, port)},
		command,
	)
}

func sanitizeIperfError(stderr string) string {
	return strings.TrimSpace(iperfErrorPrefixPattern.ReplaceAllString(stderr, "${1}"))
}

func sanitizeCurlError(stderr string) string {
	return strings.TrimSpace(curlErrorPrefixPattern.ReplaceAllString(stderr, "${1}"))
}

func (s *Service) runTCSet(ctx context.Context, command, nodeID, interfaceName string, args []string) (commandResponse, error) {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	var targetIface model.Interface
	found := false
	for _, iface := range node.Interfaces {
		if iface.Name != interfaceName {
			continue
		}
		targetIface = iface
		found = true
		break
	}
	if !found {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}

	previousConditions := targetIface.Conditions
	conditions, err := parseTCSetConditions(args, targetIface.Conditions)
	if err != nil {
		return commandResponse{}, err
	}
	if !s.repo.UpdateInterfaceConditions(nodeID, interfaceName, conditions) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}
	targetIface.Conditions = conditions

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		slog.Error("Container inspect failed", "err", err)
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State != nil && inspect.State.Running && targetIface.LinkID != "" {
		if targetIface.RuntimeName == "" {
			s.syncRuntimeInterfaces(nodeID, node.Interfaces, inspect)
			if err := s.syncRuntimeInterfaceNames(ctx, nodeID); err != nil {
				return commandResponse{}, err
			}
			node, ok = s.repo.GetNode(nodeID)
			if !ok {
				return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
			}
			for _, iface := range node.Interfaces {
				if iface.Name != interfaceName {
					continue
				}
				targetIface = iface
				break
			}
		}

		if err := s.applyRuntimeInterfaceConditions(ctx, node, targetIface); err != nil {
			_ = s.repo.UpdateInterfaceConditions(nodeID, interfaceName, previousConditions)
			targetIface.Conditions = previousConditions
			_ = s.applyRuntimeInterfaceConditions(ctx, node, targetIface)
			return commandResponse{}, err
		}
	}

	return commandResponse{
		Command:  command,
		Stdout:   formatTCShowLine(targetIface),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) runTCClear(ctx context.Context, command, nodeID, interfaceName string) (commandResponse, error) {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	var targetIface model.Interface
	found := false
	for _, iface := range node.Interfaces {
		if iface.Name != interfaceName {
			continue
		}
		targetIface = iface
		found = true
		break
	}
	if !found {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}

	previousConditions := targetIface.Conditions
	if !s.repo.UpdateInterfaceConditions(nodeID, interfaceName, model.TrafficConditions{}) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		slog.Error("Container inspect failed", "err", err)
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State != nil && inspect.State.Running && targetIface.LinkID != "" {
		if targetIface.RuntimeName == "" {
			s.syncRuntimeInterfaces(nodeID, node.Interfaces, inspect)
			if err := s.syncRuntimeInterfaceNames(ctx, nodeID); err != nil {
				return commandResponse{}, err
			}
			node, ok = s.repo.GetNode(nodeID)
			if !ok {
				return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
			}
			for _, iface := range node.Interfaces {
				if iface.Name != interfaceName {
					continue
				}
				targetIface = iface
				break
			}
		}
		if targetIface.RuntimeName != "" {
			if err := s.clearRuntimeInterfaceConditions(ctx, node.ContainerID, targetIface.RuntimeName); err != nil {
				_ = s.repo.UpdateInterfaceConditions(nodeID, interfaceName, previousConditions)
				targetIface.Conditions = previousConditions
				_ = s.applyRuntimeInterfaceConditions(ctx, node, targetIface)
				return commandResponse{}, err
			}
		}
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("%s tc conditions cleared", interfaceName),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) runTCShow(command string, node model.Node, interfaceName string) (commandResponse, error) {
	for _, iface := range node.Interfaces {
		if iface.Name != interfaceName {
			continue
		}
		return commandResponse{
			Command:  command,
			Stdout:   formatTCShowLine(iface),
			Stderr:   "",
			ExitCode: 0,
		}, nil
	}

	return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
}

func formatTCShowLine(iface model.Interface) string {
	return fmt.Sprintf(
		"%s: delay=%dms jitter=%dms loss=%s%% bandwidth=%dkbit",
		iface.Name,
		iface.Conditions.DelayMs,
		iface.Conditions.JitterMs,
		strconv.FormatFloat(iface.Conditions.LossPct, 'f', -1, 64),
		iface.Conditions.BandwidthKbit,
	)
}

func (s *Service) execBoolCommand(ctx context.Context, containerID, shellCmd, failureMessage string) (bool, error) {
	stdout, stderr, exitCode, err := execInContainer(
		ctx,
		s.docker,
		containerID,
		[]string{"sh", "-c", shellCmd},
	)
	if err != nil {
		return false, err
	}
	if exitCode != 0 {
		message := failureMessage
		if trimmed := strings.TrimSpace(stderr); trimmed != "" {
			message += ": " + trimmed
		}
		slog.Error("Container exec failed", "message", message)
		return false, httputil.NewAppError(http.StatusInternalServerError, message)
	}

	return strings.TrimSpace(stdout) == "true", nil
}

func (s *Service) serverRunningFromPIDFile(ctx context.Context, containerID, pidFilePath, failureMessage string) (bool, error) {
	return s.execBoolCommand(
		ctx,
		containerID,
		fmt.Sprintf("if [ ! -f %s ]; then echo false; elif kill -0 \"$(cat %s)\" >/dev/null 2>&1; then echo true; else echo false; fi", pidFilePath, pidFilePath),
		failureMessage,
	)
}

func (s *Service) tcpServerRunning(ctx context.Context, containerID string) (bool, error) {
	return s.serverRunningFromPIDFile(ctx, containerID, tcpPIDFilePath, "failed to inspect tcp server status")
}

func (s *Service) udpServerRunning(ctx context.Context, containerID string) (bool, error) {
	return s.serverRunningFromPIDFile(ctx, containerID, udpPIDFilePath, "failed to inspect udp server status")
}

func (s *Service) processOwnsPort(ctx context.Context, containerID string, port int, proto, processName string) (bool, error) {
	var shellCmd string
	switch proto {
	case "tcp":
		shellCmd = fmt.Sprintf(`ss -ltnpH "( sport = :%d )" | grep -q '%s' && echo true || echo false`, port, processName)
	case "udp":
		shellCmd = fmt.Sprintf(`ss -lunpH "( sport = :%d )" | grep -q '%s' && echo true || echo false`, port, processName)
	default:
		return false, httputil.NewAppError(http.StatusInternalServerError, "unsupported transport")
	}

	return s.execBoolCommand(ctx, containerID, shellCmd, fmt.Sprintf("failed to inspect %s port %d", proto, port))
}

func (s *Service) iperfOwnsPort(ctx context.Context, containerID string, port int) (bool, error) {
	if port != 5201 {
		return false, nil
	}
	return s.iperfServerRunning(ctx, containerID)
}

func (s *Service) ensureListenerAvailable(ctx context.Context, containerID string, kind listenerKind, port int) error {
	iperfBusy, err := s.iperfOwnsPort(ctx, containerID, port)
	if err != nil {
		return err
	}
	httpBusy, err := s.processOwnsPort(ctx, containerID, port, "tcp", "darkhttpd")
	if err != nil {
		return err
	}
	tcpBusyByNC, err := s.processOwnsPort(ctx, containerID, port, "tcp", "nc")
	if err != nil {
		return err
	}
	udpBusyByNC, err := s.processOwnsPort(ctx, containerID, port, "udp", "nc")
	if err != nil {
		return err
	}
	tcpBusy, err := s.portBusy(ctx, containerID, port, "tcp")
	if err != nil {
		return err
	}
	udpBusy, err := s.portBusy(ctx, containerID, port, "udp")
	if err != nil {
		return err
	}

	switch kind {
	case listenerIperf:
		if httpBusy {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("port %d is busy by http server", port))
		}
		if tcpBusyByNC {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("port %d is busy by tcp server", port))
		}
		if udpBusyByNC {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("port %d is busy by udp server", port))
		}
		if tcpBusy || udpBusy {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("port %d is busy", port))
		}
	case listenerHTTP:
		if iperfBusy {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("port %d is busy by iperf server", port))
		}
		if tcpBusyByNC {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("port %d is busy by tcp server", port))
		}
		if httpBusy || tcpBusy {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("tcp port %d is busy", port))
		}
	case listenerTCP:
		if iperfBusy {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("port %d is busy by iperf server", port))
		}
		if httpBusy {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("port %d is busy by http server", port))
		}
		if tcpBusyByNC || tcpBusy {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("tcp port %d is busy", port))
		}
	case listenerUDP:
		if iperfBusy {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("port %d is busy by iperf server", port))
		}
		if udpBusyByNC || udpBusy {
			return httputil.NewAppError(http.StatusBadRequest, fmt.Sprintf("udp port %d is busy", port))
		}
	default:
		return httputil.NewAppError(http.StatusInternalServerError, "unsupported listener kind")
	}

	return nil
}

func (s *Service) portBusy(ctx context.Context, containerID string, port int, proto string) (bool, error) {
	var shellCmd string
	switch proto {
	case "tcp":
		shellCmd = fmt.Sprintf(`ss -ltnH "( sport = :%d )" | wc -l`, port)
	case "udp":
		shellCmd = fmt.Sprintf(`ss -lunH "( sport = :%d )" | wc -l`, port)
	default:
		return false, httputil.NewAppError(http.StatusInternalServerError, "unsupported transport")
	}

	stdout, stderr, exitCode, err := execInContainer(
		ctx,
		s.docker,
		containerID,
		[]string{"sh", "-c", shellCmd},
	)
	if err != nil {
		return false, err
	}
	if exitCode != 0 {
		message := fmt.Sprintf("failed to inspect %s port %d", proto, port)
		if trimmed := strings.TrimSpace(stderr); trimmed != "" {
			message += ": " + trimmed
		}
		slog.Error("Container exec failed", "message", message)
		return false, httputil.NewAppError(http.StatusInternalServerError, message)
	}

	return strings.TrimSpace(stdout) != "0", nil
}

func validatePort(raw string) (int, error) {
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, httputil.NewAppError(http.StatusBadRequest, "invalid port")
	}
	if port < 1 || port > 65535 {
		return 0, httputil.NewAppError(http.StatusBadRequest, "port out of range")
	}

	return port, nil
}

func parseIperfClientArgs(args []string, allowUDPOptions bool) ([]string, error) {
	hasTime := false
	hasBytes := false
	hasBitrate := false
	hasPacketLength := false
	timeSeconds := 5
	byteCount := 0
	bitrate := ""
	packetLength := 0

	for index := 0; index < len(args); {
		flagName := args[index]
		if index+1 >= len(args) {
			return nil, httputil.NewAppError(http.StatusBadRequest, "missing iperf flag value for "+flagName)
		}
		value := args[index+1]

		switch flagName {
		case "--time":
			if hasTime {
				return nil, httputil.NewAppError(http.StatusBadRequest, "duplicate iperf flag: --time")
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return nil, httputil.NewAppError(http.StatusBadRequest, "time must be a positive integer")
			}
			hasTime = true
			timeSeconds = parsed
		case "--bytes":
			if hasBytes {
				return nil, httputil.NewAppError(http.StatusBadRequest, "duplicate iperf flag: --bytes")
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return nil, httputil.NewAppError(http.StatusBadRequest, "bytes must be a positive integer")
			}
			hasBytes = true
			byteCount = parsed
		case "--bitrate":
			if !allowUDPOptions {
				return nil, httputil.NewAppError(http.StatusBadRequest, "--bitrate is only supported for iperf udp")
			}
			if hasBitrate {
				return nil, httputil.NewAppError(http.StatusBadRequest, "duplicate iperf flag: --bitrate")
			}
			if !iperfBitratePattern.MatchString(value) {
				return nil, httputil.NewAppError(http.StatusBadRequest, "bitrate must be a positive number with optional K, M, or G suffix")
			}
			hasBitrate = true
			bitrate = value
		case "--packet-length":
			if !allowUDPOptions {
				return nil, httputil.NewAppError(http.StatusBadRequest, "--packet-length is only supported for iperf udp")
			}
			if hasPacketLength {
				return nil, httputil.NewAppError(http.StatusBadRequest, "duplicate iperf flag: --packet-length")
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return nil, httputil.NewAppError(http.StatusBadRequest, "packet length must be a positive integer")
			}
			if parsed < minIperfUDPPacketLength || parsed > maxIperfUDPPacketLength {
				return nil, httputil.NewAppError(http.StatusBadRequest, "packet length must be between 16 and 65507 bytes")
			}
			hasPacketLength = true
			packetLength = parsed
		default:
			return nil, httputil.NewAppError(http.StatusBadRequest, "unsupported iperf flag: "+flagName)
		}

		index += 2
	}
	if hasTime && hasBytes {
		return nil, httputil.NewAppError(http.StatusBadRequest, "--time and --bytes are mutually exclusive")
	}
	out := make([]string, 0, 6)
	if hasBytes {
		out = append(out, "-n", strconv.Itoa(byteCount))
	} else {
		out = append(out, "-t", strconv.Itoa(timeSeconds))
	}
	if hasBitrate {
		out = append(out, "-b", bitrate)
	}
	if hasPacketLength {
		out = append(out, "-l", strconv.Itoa(packetLength))
	}

	return out, nil
}

func ValidateTrafficConditions(conditions model.TrafficConditions) error {
	if conditions.DelayMs < 0 {
		return httputil.NewAppError(http.StatusBadRequest, "delay must be non-negative")
	}
	if conditions.JitterMs < 0 {
		return httputil.NewAppError(http.StatusBadRequest, "jitter must be non-negative")
	}
	if math.IsNaN(conditions.LossPct) || math.IsInf(conditions.LossPct, 0) {
		return httputil.NewAppError(http.StatusBadRequest, "loss must be finite")
	}
	if conditions.LossPct < 0 || conditions.LossPct > 100 {
		return httputil.NewAppError(http.StatusBadRequest, "loss must be between 0 and 100")
	}
	if conditions.BandwidthKbit < 0 {
		return httputil.NewAppError(http.StatusBadRequest, "bandwidth must be non-negative")
	}

	return nil
}

func hasTrafficNetemConditions(conditions model.TrafficConditions) bool {
	return conditions.DelayMs > 0 || conditions.JitterMs > 0 || conditions.LossPct > 0
}

func buildTrafficNetemArgs(conditions model.TrafficConditions) []string {
	args := make([]string, 0, 6)
	if conditions.DelayMs > 0 {
		args = append(args, "delay", fmt.Sprintf("%dms", conditions.DelayMs))
		if conditions.JitterMs > 0 {
			args = append(args, fmt.Sprintf("%dms", conditions.JitterMs))
		}
	}
	if conditions.LossPct > 0 {
		args = append(args, "loss", strconv.FormatFloat(conditions.LossPct, 'f', -1, 64)+"%")
	}

	return args
}

func parseTCSetConditions(args []string, current model.TrafficConditions) (model.TrafficConditions, error) {
	if len(args) == 0 {
		return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "tc set requires at least one flag")
	}

	conditions := current
	seen := make(map[string]struct{}, 4)

	for index := 0; index < len(args); {
		flagName := args[index]
		if _, ok := seen[flagName]; ok {
			return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "duplicate tc flag: "+flagName)
		}
		seen[flagName] = struct{}{}
		if index+1 >= len(args) {
			return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "missing tc flag value for "+flagName)
		}

		value := args[index+1]
		switch flagName {
		case "--delay":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid delay value")
			}
			conditions.DelayMs = parsed
		case "--jitter":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid jitter value")
			}
			conditions.JitterMs = parsed
		case "--loss":
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid loss value")
			}
			conditions.LossPct = parsed
		case "--bandwidth":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid bandwidth value")
			}
			conditions.BandwidthKbit = parsed
		default:
			return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "unsupported tc flag: "+flagName)
		}

		index += 2
	}

	if err := ValidateTrafficConditions(conditions); err != nil {
		return model.TrafficConditions{}, err
	}

	return conditions, nil
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

func (s *Service) runIPUnset(ctx context.Context, command, nodeID, interfaceName string) (commandResponse, error) {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	found := false
	targetIface := model.Interface{}
	for _, iface := range node.Interfaces {
		if iface.Name != interfaceName {
			continue
		}
		found = true
		targetIface = iface
		break
	}
	if !found {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}
	if targetIface.IPAddr == "" || targetIface.PrefixLen == 0 {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface is already unassigned")
	}

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		slog.Error("Container inspect failed", "err", err)
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	if inspect.State != nil && inspect.State.Running && targetIface.LinkID != "" {
		if targetIface.RuntimeName == "" {
			s.syncRuntimeInterfaces(nodeID, node.Interfaces, inspect)
			if err := s.syncRuntimeInterfaceNames(ctx, nodeID); err != nil {
				return commandResponse{}, err
			}

			node, ok = s.repo.GetNode(nodeID)
			if !ok {
				return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
			}
			for _, iface := range node.Interfaces {
				if iface.Name != interfaceName {
					continue
				}
				targetIface = iface
				break
			}
		}

		if err := s.deleteRuntimeInterfaceAddress(ctx, node, targetIface); err != nil {
			return commandResponse{}, err
		}
	}

	if !s.repo.ClearInterfaceAddress(nodeID, interfaceName) {
		slog.Error("Failed to clear interface address")
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "failed to clear interface address")
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("%s unset", interfaceName),
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

func (s *Service) deleteRuntimeInterfaceAddress(ctx context.Context, node model.Node, iface model.Interface) error {
	if iface.RuntimeName == "" || iface.IPAddr == "" || iface.PrefixLen == 0 {
		return nil
	}

	cidr := fmt.Sprintf("%s/%d", iface.IPAddr, iface.PrefixLen)
	stdout, stderr, exitCode, err := execInContainer(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "addr", "del", cidr, "dev", iface.RuntimeName},
	)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		combined := strings.TrimSpace(stderr)
		if combined == "" {
			combined = strings.TrimSpace(stdout)
		}
		if strings.Contains(combined, "Cannot assign requested address") || strings.Contains(combined, "Cannot find device") {
			return nil
		}
		message := "failed to remove runtime interface address"
		if combined != "" {
			message += ": " + combined
		}
		slog.Error("Container exec failed", "message", message)
		return httputil.NewAppError(http.StatusInternalServerError, message)
	}

	return nil
}

func (s *Service) applyRuntimeInterfaceConditions(ctx context.Context, node model.Node, iface model.Interface) error {
	if iface.LinkID == "" || iface.RuntimeName == "" {
		return nil
	}

	if err := s.clearRuntimeInterfaceConditions(ctx, node.ContainerID, iface.RuntimeName); err != nil {
		return err
	}
	if iface.Conditions.BandwidthKbit > 0 {
		rate := fmt.Sprintf("%dkbit", iface.Conditions.BandwidthKbit)
		if _, err := execInContainerChecked(
			ctx,
			s.docker,
			node.ContainerID,
			[]string{"tc", "qdisc", "replace", "dev", iface.RuntimeName, "root", "handle", "1:", "htb", "default", "1"},
			"failed to apply tc root qdisc",
		); err != nil {
			return err
		}
		if _, err := execInContainerChecked(
			ctx,
			s.docker,
			node.ContainerID,
			[]string{"tc", "class", "replace", "dev", iface.RuntimeName, "parent", "1:", "classid", "1:1", "htb", "rate", rate, "ceil", rate},
			"failed to apply tc bandwidth class",
		); err != nil {
			return err
		}
		if hasTrafficNetemConditions(iface.Conditions) {
			execCmd := append([]string{"tc", "qdisc", "replace", "dev", iface.RuntimeName, "parent", "1:1", "handle", "10:", "netem"}, buildTrafficNetemArgs(iface.Conditions)...)
			if _, err := execInContainerChecked(ctx, s.docker, node.ContainerID, execCmd, "failed to apply tc netem conditions"); err != nil {
				return err
			}
		}
		return nil
	}
	if hasTrafficNetemConditions(iface.Conditions) {
		execCmd := append([]string{"tc", "qdisc", "replace", "dev", iface.RuntimeName, "root", "netem"}, buildTrafficNetemArgs(iface.Conditions)...)
		if _, err := execInContainerChecked(ctx, s.docker, node.ContainerID, execCmd, "failed to apply tc netem conditions"); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) clearRuntimeInterfaceConditions(ctx context.Context, containerID, runtimeName string) error {
	stdout, stderr, exitCode, err := execInContainer(
		ctx,
		s.docker,
		containerID,
		[]string{"tc", "qdisc", "del", "dev", runtimeName, "root"},
	)
	if err != nil {
		return err
	}
	if exitCode == 0 {
		return nil
	}

	combined := strings.TrimSpace(stderr)
	if combined == "" {
		combined = strings.TrimSpace(stdout)
	}
	if strings.Contains(combined, "No such file or directory") || strings.Contains(combined, "Cannot delete qdisc with handle of zero") {
		return nil
	}

	message := "failed to clear tc conditions"
	if combined != "" {
		message += ": " + combined
	}
	slog.Error("Container exec failed", "message", message)
	return httputil.NewAppError(http.StatusInternalServerError, message)
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

func (s *Service) runIPRouteDelete(ctx context.Context, command string, node model.Node, target string) (commandResponse, error) {
	destination := target
	if target == "default" {
		destination = "0.0.0.0/0"
	} else {
		prefix, err := netip.ParsePrefix(target)
		if err != nil {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid route destination")
		}
		destination = prefix.Masked().String()
	}

	var route model.Route
	found := false
	for _, existing := range node.Routes {
		if existing.Destination != destination {
			continue
		}
		route = existing
		found = true
		break
	}
	if !found {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "route not found")
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
		if err := s.deleteRuntimeRoute(ctx, node, route); err != nil {
			return commandResponse{}, err
		}
	}

	if !s.repo.DeleteRoute(node.ID, destination) {
		slog.Error("Failed to delete route")
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "failed to delete route")
	}

	label := route.Destination
	if route.Destination == "0.0.0.0/0" {
		label = "default"
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("route %s deleted", label),
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

func (s *Service) deleteRuntimeRoute(ctx context.Context, node model.Node, route model.Route) error {
	destination := route.Destination
	if destination == "0.0.0.0/0" {
		destination = "default"
	}

	stdout, stderr, exitCode, err := execInContainer(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "route", "del", destination, "via", route.NextHop},
	)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		combined := strings.TrimSpace(stderr)
		if combined == "" {
			combined = strings.TrimSpace(stdout)
		}
		if strings.Contains(combined, "No such process") {
			return nil
		}
		message := "failed to delete runtime route"
		if combined != "" {
			message += ": " + combined
		}
		slog.Error("Container exec failed", "message", message)
		return httputil.NewAppError(http.StatusInternalServerError, message)
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
