package links

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
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

type Service struct {
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

func NewService(docker *client.Client, s *store.Store) *Service {
	return &Service{docker: docker, repo: NewRepository(s)}
}

func (s *Service) CreateLink(ctx context.Context, interfaceAID, interfaceBID string) (model.Link, error) {
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
	subnet, err := s.repo.store.LinkSubnets.Allocate()
	if err != nil {
		slog.Error("Link subnet allocation failed", "err", err)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "link subnet allocation failed")
	}
	gateway, err := store.GatewayAddr(subnet)
	if err != nil {
		s.repo.store.LinkSubnets.Release(subnet)
		slog.Error("Link subnet gateway resolution failed", "err", err)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "link subnet gateway resolution failed")
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
		s.repo.store.LinkSubnets.Release(subnet)
		slog.Error("Network create failed", "err", err)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, fmt.Sprintf("network create failed: %v", err))
	}

	if err := s.docker.NetworkConnect(ctx, networkResp.ID, nodeA.ContainerID, nil); err != nil {
		s.rollbackLinkCreate(ctx, networkResp.ID, subnet, nodeA.ContainerID)
		slog.Error("Network connect failed", "err", err)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "network connect failed")
	}
	if err := s.docker.NetworkConnect(ctx, networkResp.ID, nodeB.ContainerID, nil); err != nil {
		s.rollbackLinkCreate(ctx, networkResp.ID, subnet, nodeA.ContainerID, nodeB.ContainerID)
		slog.Error("Network connect failed", "err", err)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "network connect failed")
	}

	inspectA, err := s.docker.ContainerInspect(ctx, nodeA.ContainerID)
	if err != nil {
		s.rollbackLinkCreate(ctx, networkResp.ID, subnet, nodeA.ContainerID, nodeB.ContainerID)
		slog.Error("Container inspect failed", "err", err)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	endpointA, ok := inspectA.NetworkSettings.Networks[networkName]
	if !ok || endpointA == nil {
		s.rollbackLinkCreate(ctx, networkResp.ID, subnet, nodeA.ContainerID, nodeB.ContainerID)
		slog.Error("Runtime network endpoint missing")
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "runtime network endpoint missing")
	}

	inspectB, err := s.docker.ContainerInspect(ctx, nodeB.ContainerID)
	if err != nil {
		s.rollbackLinkCreate(ctx, networkResp.ID, subnet, nodeA.ContainerID, nodeB.ContainerID)
		slog.Error("Container inspect failed", "err", err)
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	endpointB, ok := inspectB.NetworkSettings.Networks[networkName]
	if !ok || endpointB == nil {
		s.rollbackLinkCreate(ctx, networkResp.ID, subnet, nodeA.ContainerID, nodeB.ContainerID)
		slog.Error("Runtime network endpoint missing")
		return model.Link{}, httputil.NewAppError(http.StatusInternalServerError, "runtime network endpoint missing")
	}

	link := model.Link{
		ID:           linkID,
		InterfaceAID: interfaceAID,
		InterfaceBID: interfaceBID,
		NetworkID:    networkResp.ID,
		NetworkName:  networkName,
		Subnet:       subnet.String(),
		CreatedAt:    time.Now().UTC(),
	}
	s.repo.AddLink(link)
	s.repo.SetInterfaceLink(interfaceAID, linkID)
	s.repo.SetInterfaceLink(interfaceBID, linkID)
	s.repo.SetInterfaceRuntime(interfaceAID, endpointA.IPAddress, endpointA.IPPrefixLen)
	s.repo.SetInterfaceRuntime(interfaceBID, endpointB.IPAddress, endpointB.IPPrefixLen)
	if err := s.realizeLinkedInterface(ctx, nodeA, interfaceAID, endpointA.IPAddress, endpointA.IPPrefixLen, ifaceA.IPAddr, ifaceA.PrefixLen, ifaceA.Conditions); err != nil {
		s.rollbackPersistedLinkCreate(ctx, link)
		return model.Link{}, err
	}
	if err := s.realizeLinkedInterface(ctx, nodeB, interfaceBID, endpointB.IPAddress, endpointB.IPPrefixLen, ifaceB.IPAddr, ifaceB.PrefixLen, ifaceB.Conditions); err != nil {
		s.rollbackPersistedLinkCreate(ctx, link)
		return model.Link{}, err
	}

	return link, nil
}

func (s *Service) listLinks() ([]model.Link, error) {
	return s.repo.ListLinks(), nil
}

func (s *Service) deleteLink(ctx context.Context, linkID string) error {
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

	if err := s.removeLinkNetwork(ctx, link); err != nil {
		return err
	}
	s.repo.SetInterfaceLink(link.InterfaceAID, "")
	s.repo.SetInterfaceLink(link.InterfaceBID, "")
	s.repo.SetInterfaceRuntime(link.InterfaceAID, "", 0)
	s.repo.SetInterfaceRuntime(link.InterfaceBID, "", 0)
	s.repo.SetInterfaceRuntimeName(link.InterfaceAID, "")
	s.repo.SetInterfaceRuntimeName(link.InterfaceBID, "")
	s.repo.DeleteLink(linkID)
	s.repo.store.LinkSubnets.ReleaseString(link.Subnet)
	return nil
}

func (s *Service) removeLinkNetwork(ctx context.Context, link model.Link) error {
	if link.NetworkID == "" {
		return nil
	}
	if nodeA, _, ok := s.repo.GetNodeByInterface(link.InterfaceAID); ok && nodeA.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeA.ContainerID, true)
	}
	if nodeB, _, ok := s.repo.GetNodeByInterface(link.InterfaceBID); ok && nodeB.ContainerID != "" {
		_ = s.docker.NetworkDisconnect(ctx, link.NetworkID, nodeB.ContainerID, true)
	}
	if err := s.docker.NetworkRemove(ctx, link.NetworkID); err != nil && !client.IsErrNotFound(err) {
		slog.Error("Link network remove failed", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "link network remove failed")
	}
	return nil
}

func (s *Service) rollbackLinkCreate(ctx context.Context, networkID string, subnet netip.Prefix, containerIDs ...string) {
	for _, containerID := range containerIDs {
		if containerID == "" {
			continue
		}
		_ = s.docker.NetworkDisconnect(ctx, networkID, containerID, true)
	}
	if networkID != "" {
		_ = s.docker.NetworkRemove(ctx, networkID)
	}
	s.repo.store.LinkSubnets.Release(subnet)
}

func (s *Service) rollbackPersistedLinkCreate(ctx context.Context, link model.Link) {
	_ = s.removeLinkNetwork(ctx, link)
	s.repo.SetInterfaceLink(link.InterfaceAID, "")
	s.repo.SetInterfaceLink(link.InterfaceBID, "")
	s.repo.SetInterfaceRuntime(link.InterfaceAID, "", 0)
	s.repo.SetInterfaceRuntime(link.InterfaceBID, "", 0)
	s.repo.SetInterfaceRuntimeName(link.InterfaceAID, "")
	s.repo.SetInterfaceRuntimeName(link.InterfaceBID, "")
	s.repo.DeleteLink(link.ID)
	s.repo.store.LinkSubnets.ReleaseString(link.Subnet)
}

func (s *Service) realizeLinkedInterface(
	ctx context.Context,
	node model.Node,
	interfaceID string,
	runtimeIP string,
	runtimePrefixLen int,
	logicalIP string,
	logicalPrefixLen int,
	conditions model.TrafficConditions,
) error {
	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		slog.Error("Container inspect failed", "err", err)
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
		slog.Error("Failed to persist runtime interface name")
		return httputil.NewAppError(http.StatusInternalServerError, "failed to persist runtime interface name")
	}
	if node.Type == model.Switch {
		if err := s.attachSwitchPort(ctx, node, runtimeName); err != nil {
			return err
		}
		return s.applyRuntimeInterfaceConditions(ctx, node.ContainerID, runtimeName, conditions)
	}
	if logicalIP != "" && logicalPrefixLen != 0 {
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
	}

	return s.applyRuntimeInterfaceConditions(ctx, node.ContainerID, runtimeName, conditions)
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
		slog.Error("Runtime interface inspect parse failed", "err", err)
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

	slog.Error("Runtime interface name resolution failed")
	return "", httputil.NewAppError(http.StatusInternalServerError, "runtime interface name resolution failed")
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

func (s *Service) detachSwitchPortIfRunning(ctx context.Context, node model.Node, runtimeName string) error {
	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		slog.Error("Container inspect failed", "err", err)
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

func hasTrafficNetemConditions(conditions model.TrafficConditions) bool {
	return conditions.DelayMs > 0 || conditions.JitterMs > 0 || conditions.LossPct > 0 || conditions.QueueLimitPackets > 0
}

func buildTrafficNetemArgs(conditions model.TrafficConditions) []string {
	args := make([]string, 0, 8)
	if conditions.DelayMs > 0 {
		args = append(args, "delay", fmt.Sprintf("%dms", conditions.DelayMs))
		if conditions.JitterMs > 0 {
			args = append(args, fmt.Sprintf("%dms", conditions.JitterMs))
		}
	}
	if conditions.LossPct > 0 {
		loss := strconv.FormatFloat(conditions.LossPct, 'f', -1, 64) + "%"
		if conditions.LossCorrelationPct > 0 {
			args = append(args, "loss", loss, strconv.FormatFloat(conditions.LossCorrelationPct, 'f', -1, 64)+"%")
		} else {
			args = append(args, "loss", loss)
		}
	}
	if conditions.QueueLimitPackets > 0 {
		args = append(args, "limit", strconv.Itoa(conditions.QueueLimitPackets))
	}

	return args
}

func (s *Service) applyRuntimeInterfaceConditions(ctx context.Context, containerID, runtimeName string, conditions model.TrafficConditions) error {
	if runtimeName == "" {
		return nil
	}

	if err := s.clearRuntimeInterfaceConditions(ctx, containerID, runtimeName); err != nil {
		return err
	}
	if conditions.BandwidthKbit > 0 {
		rate := fmt.Sprintf("%dkbit", conditions.BandwidthKbit)
		if _, err := execInContainerChecked(
			ctx,
			s.docker,
			containerID,
			[]string{"tc", "qdisc", "replace", "dev", runtimeName, "root", "handle", "1:", "htb", "default", "1"},
			"failed to apply tc root qdisc",
		); err != nil {
			return err
		}
		if _, err := execInContainerChecked(
			ctx,
			s.docker,
			containerID,
			[]string{"tc", "class", "replace", "dev", runtimeName, "parent", "1:", "classid", "1:1", "htb", "rate", rate, "ceil", rate},
			"failed to apply tc bandwidth class",
		); err != nil {
			return err
		}
		if hasTrafficNetemConditions(conditions) {
			execCmd := append([]string{"tc", "qdisc", "replace", "dev", runtimeName, "parent", "1:1", "handle", "10:", "netem"}, buildTrafficNetemArgs(conditions)...)
			if _, err := execInContainerChecked(ctx, s.docker, containerID, execCmd, "failed to apply tc netem conditions"); err != nil {
				return err
			}
		}
		return nil
	}
	if hasTrafficNetemConditions(conditions) {
		execCmd := append([]string{"tc", "qdisc", "replace", "dev", runtimeName, "root", "netem"}, buildTrafficNetemArgs(conditions)...)
		if _, err := execInContainerChecked(ctx, s.docker, containerID, execCmd, "failed to apply tc netem conditions"); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) clearRuntimeInterfaceConditions(ctx context.Context, containerID, runtimeName string) error {
	stdout, stderr, exitCode, err := execInContainer(ctx, s.docker, containerID, []string{"tc", "qdisc", "del", "dev", runtimeName, "root"})
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
