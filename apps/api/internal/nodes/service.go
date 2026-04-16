package nodes

import (
	"bytes"
	"context"
	"encoding/json"
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

func (s *service) createNode(ctx context.Context, reqNodeType string) (model.Node, error) {
	nodeType, ok := model.NameNodeType[reqNodeType]
	if !ok {
		return model.Node{}, httputil.NewAppError(http.StatusBadRequest, "invalid node type")
	}
	image, ifaceCount := nodeTypeTag(nodeType)

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
		if err := s.syncRuntimeInterfaceNames(ctx, nodeID); err != nil {
			return err
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
	if err := s.syncRuntimeInterfaceNames(ctx, nodeID); err != nil {
		return err
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

type runtimeAddrInfo struct {
	Local     string `json:"local"`
	PrefixLen int    `json:"prefixlen"`
}

type runtimeInterfaceInfo struct {
	IfName   string            `json:"ifname"`
	AddrInfo []runtimeAddrInfo `json:"addr_info"`
}

func (s *service) syncRuntimeInterfaceNames(ctx context.Context, nodeID string) error {
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
			return httputil.NewAppError(http.StatusInternalServerError, "runtime interface name resolution failed")
		}
		if !s.repo.UpdateInterfaceRuntimeName(nodeID, iface.ID, runtimeName) {
			return httputil.NewAppError(http.StatusInternalServerError, "failed to persist runtime interface name")
		}
	}

	return nil
}

func (s *service) syncRuntimeInterfaceAddresses(ctx context.Context, nodeID string) error {
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

func (s *service) syncRuntimeRoutes(ctx context.Context, nodeID string) error {
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

func execInContainer(ctx context.Context, docker *client.Client, containerID string, execCmd []string) (string, string, int, error) {
	execResp, err := docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          execCmd,
	})
	if err != nil {
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec create failed")
	}

	attachResp, err := docker.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec attach failed")
	}
	defer attachResp.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		return "", "", 0, httputil.NewAppError(http.StatusInternalServerError, "exec read failed")
	}

	execInspect, err := docker.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
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
		return "", httputil.NewAppError(http.StatusInternalServerError, message)
	}
	return stdout, nil
}

func (s *service) execCommand(ctx context.Context, containerID string, execCmd []string, command string) (commandResponse, error) {
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
	if command == "help" {
		return runHelp(command), nil
	}

	fields := strings.Fields(command)
	if len(fields) == 2 && fields[0] == "ping" {
		return s.runPing(ctx, command, node)
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

func runHelp(command string) commandResponse {
	lines := []string{
		"help",
		"ip addr",
		"ip set [interface] [ip/prefix]",
		"ip route",
		"ip route default [next-hop]",
		"ip route add [destination/prefix] via [next-hop]",
		"ping [target-ip]",
	}

	return commandResponse{
		Command:  command,
		Stdout:   strings.Join(lines, "\n"),
		Stderr:   "",
		ExitCode: 0,
	}
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

	if !s.canReachTargetLocked(node.ID, targetAddr, targetLogicalIP, map[string]struct{}{}) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "target ip is unreachable")
	}

	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"ping", "-c", "4", targetLogicalIP},
		command,
	)
}

func (s *service) runIPSet(ctx context.Context, command, nodeID, interfaceName, cidr string) (commandResponse, error) {
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

func (s *service) applyRuntimeInterfaceAddress(ctx context.Context, node model.Node, iface model.Interface) error {
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

func (s *service) runIPRoute(
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
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "failed to persist route")
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("route %s via %s configured", route.Destination, route.NextHop),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *service) applyRuntimeRoute(ctx context.Context, node model.Node, route model.Route) error {
	s.repo.store.Mu.RLock()
	defer s.repo.store.Mu.RUnlock()

	_, _, ok := s.findDirectNextHopLocked(node, route.NextHop)
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

func (s *service) canReachTargetLocked(
	nodeID string,
	targetAddr netip.Addr,
	targetLogicalIP string,
	visited map[string]struct{},
) bool {
	if _, ok := visited[nodeID]; ok {
		return false
	}
	visited[nodeID] = struct{}{}

	node, ok := s.repo.store.Nodes[nodeID]
	if !ok {
		return false
	}

	if s.hasDirectTargetLocked(node, targetAddr, targetLogicalIP) {
		return true
	}

	route, ok := s.findBestRouteLocked(node, targetAddr)
	if !ok {
		return false
	}

	nextHopNode, _, ok := s.findDirectNextHopLocked(node, route.NextHop)
	if !ok {
		return false
	}

	return s.canReachTargetLocked(nextHopNode.ID, targetAddr, targetLogicalIP, visited)
}

func (s *service) hasDirectTargetLocked(node model.Node, targetAddr netip.Addr, targetLogicalIP string) bool {
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

		link, ok := s.repo.store.Links[sourceIface.LinkID]
		if !ok {
			continue
		}

		targetInterfaceID := link.InterfaceAID
		if targetInterfaceID == sourceIface.ID {
			targetInterfaceID = link.InterfaceBID
		}

		_, candidateIface, ok := s.findInterfaceOwnerLocked(targetInterfaceID)
		if !ok || candidateIface.IPAddr != targetLogicalIP {
			continue
		}

		return true
	}

	return false
}

func (s *service) findBestRouteLocked(node model.Node, targetAddr netip.Addr) (model.Route, bool) {
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

func (s *service) findDirectNextHopLocked(node model.Node, nextHop string) (model.Node, model.Interface, bool) {
	for _, sourceIface := range node.Interfaces {
		if sourceIface.LinkID == "" {
			continue
		}

		link, ok := s.repo.store.Links[sourceIface.LinkID]
		if !ok {
			continue
		}

		peerInterfaceID := link.InterfaceAID
		if peerInterfaceID == sourceIface.ID {
			peerInterfaceID = link.InterfaceBID
		}

		peerNode, peerIface, ok := s.findInterfaceOwnerLocked(peerInterfaceID)
		if !ok || peerIface.IPAddr != nextHop {
			continue
		}

		return peerNode, peerIface, true
	}

	return model.Node{}, model.Interface{}, false
}

func (s *service) findInterfaceOwnerLocked(interfaceID string) (model.Node, model.Interface, bool) {
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
