package links

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

type service struct {
	docker *client.Client
	repo   *Repository
}

type runtimeAddrInfo struct {
	Local     string `json:"local"`
	PrefixLen int    `json:"prefixlen"`
}

type runtimeInterfaceInfo struct {
	IfName   string            `json:"ifname"`
	AddrInfo []runtimeAddrInfo `json:"addr_info"`
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
	if err := s.realizeLinkedInterface(ctx, nodeA, interfaceAID, endpointA.IPAddress, endpointA.IPPrefixLen, ifaceA.IPAddr, ifaceA.PrefixLen); err != nil {
		return model.Link{}, err
	}
	if err := s.realizeLinkedInterface(ctx, nodeB, interfaceBID, endpointB.IPAddress, endpointB.IPPrefixLen, ifaceB.IPAddr, ifaceB.PrefixLen); err != nil {
		return model.Link{}, err
	}

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

	if nodeA, ifaceA, ok := s.repo.GetNodeByInterface(link.InterfaceAID); ok && nodeA.Type == model.Switch && ifaceA.RuntimeName != "" {
		if err := s.detachSwitchPortIfRunning(ctx, nodeA, ifaceA.RuntimeName); err != nil {
			return err
		}
	}
	if nodeB, ifaceB, ok := s.repo.GetNodeByInterface(link.InterfaceBID); ok && nodeB.Type == model.Switch && ifaceB.RuntimeName != "" {
		if err := s.detachSwitchPortIfRunning(ctx, nodeB, ifaceB.RuntimeName); err != nil {
			return err
		}
	}

	s.removeLinkNetwork(ctx, link)
	s.repo.SetInterfaceLink(link.InterfaceAID, "")
	s.repo.SetInterfaceLink(link.InterfaceBID, "")
	s.repo.SetInterfaceRuntime(link.InterfaceAID, "", 0)
	s.repo.SetInterfaceRuntime(link.InterfaceBID, "", 0)
	s.repo.SetInterfaceRuntimeName(link.InterfaceAID, "")
	s.repo.SetInterfaceRuntimeName(link.InterfaceBID, "")
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

func (s *service) realizeLinkedInterface(
	ctx context.Context,
	node model.Node,
	interfaceID string,
	runtimeIP string,
	runtimePrefixLen int,
	logicalIP string,
	logicalPrefixLen int,
) error {
	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		return httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State == nil || !inspect.State.Running {
		return nil
	}

	runtimeName, err := resolveRuntimeInterfaceName(ctx, s.docker, node.ContainerID, runtimeIP, runtimePrefixLen)
	if err != nil {
		return err
	}
	if !s.repo.SetInterfaceRuntimeName(interfaceID, runtimeName) {
		return httputil.NewAppError(http.StatusInternalServerError, "failed to persist runtime interface name")
	}
	if node.Type == model.Switch {
		return s.attachSwitchPort(ctx, node, runtimeName)
	}
	if logicalIP == "" || logicalPrefixLen == 0 {
		return nil
	}

	cidr := logicalIP + "/" + strconv.Itoa(logicalPrefixLen)
	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "addr", "replace", cidr, "dev", runtimeName},
		"failed to apply runtime interface address",
	); err != nil {
		return err
	}
	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "link", "set", runtimeName, "up"},
		"failed to bring runtime interface up",
	); err != nil {
		return err
	}

	return nil
}

func resolveRuntimeInterfaceName(
	ctx context.Context,
	docker *client.Client,
	containerID string,
	runtimeIP string,
	runtimePrefixLen int,
) (string, error) {
	stdout, err := execInContainerChecked(
		ctx,
		docker,
		containerID,
		[]string{"ip", "-j", "addr", "show"},
		"failed to inspect runtime interfaces",
	)
	if err != nil {
		return "", err
	}

	var runtimeIfaces []runtimeInterfaceInfo
	if err := json.Unmarshal([]byte(stdout), &runtimeIfaces); err != nil {
		return "", httputil.NewAppError(http.StatusInternalServerError, "runtime interface inspect parse failed")
	}

	targetAddr := runtimeIP + "/" + strconv.Itoa(runtimePrefixLen)
	for _, runtimeIface := range runtimeIfaces {
		for _, addrInfo := range runtimeIface.AddrInfo {
			if addrInfo.Local+"/"+strconv.Itoa(addrInfo.PrefixLen) == targetAddr {
				return runtimeIface.IfName, nil
			}
		}
	}

	return "", httputil.NewAppError(http.StatusInternalServerError, "runtime interface name resolution failed")
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

func (s *service) ensureSwitchBridge(ctx context.Context, node model.Node) error {
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

func (s *service) attachSwitchPort(ctx context.Context, node model.Node, runtimeName string) error {
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

func (s *service) detachSwitchPortIfRunning(ctx context.Context, node model.Node, runtimeName string) error {
	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		return httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}
	if inspect.State == nil || !inspect.State.Running || runtimeName == "" {
		return nil
	}

	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "link", "set", runtimeName, "nomaster"},
		"failed to detach switch port from bridge",
	); err != nil {
		return err
	}

	return nil
}
