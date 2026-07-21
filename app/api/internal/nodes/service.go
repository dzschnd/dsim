package nodes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
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
	docker      *client.Client
	repo        *repository
	linkRepo    linkRepository
	imageCache  sync.Map // map[image]struct{}: images confirmed to exist this session
}

const (
	iperfLogPath        = "/var/log/iperf/iperf.log"
	httpLogPath         = "/var/log/http/http.log"
	httpPIDFilePath     = "/var/run/http-server.pid"
	httpPortFilePath    = "/var/run/http-server.port"
	tcpPIDFilePath      = "/var/run/tcp-server.pid"
	udpPIDFilePath      = "/var/run/udp-server.pid"
	defaultFlapDownMs   = 1000
	defaultFlapUpMs     = 1000
	defaultFlapJitterMs = 200

	minIperfUDPPacketLength = 16
	maxIperfUDPPacketLength = 65507

	httpDefaultPort               = 8080
	tcpDefaultPort                = 3000
	udpDefaultPort                = 4000
	maxSyncCommandDurationSeconds = 600
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

	if _, loaded := s.imageCache.LoadOrStore(image, struct{}{}); !loaded {
		if _, _, err := s.docker.ImageInspectWithRaw(ctx, image); err != nil {
			s.imageCache.Delete(image)
			if client.IsErrNotFound(err) {
				return model.Node{}, httputil.NewAppError(http.StatusNotFound, "image not found: "+image)
			}
			slog.Error("Image inspect failed", "err", err)
			return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "image inspect failed")
		}
	}

	nodeID := store.NewID("node_")

	initEnabled := true
	hostConfig := &container.HostConfig{
		Init:        &initEnabled,
		CapAdd:      []string{"NET_ADMIN"},
		NetworkMode: "none",
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
		nil,
		nil, "",
	)
	if err != nil {
		slog.Error("Container create failed", "err", err)
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "container create failed")
	}

	slog.Info("Container created", "node_id", nodeID, "type", reqNodeType)

	node := model.Node{
		ID:          nodeID,
		Name:        s.repo.store.NextDefaultNodeName(nodeType),
		Position:    position,
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

func (s *Service) UpdateNodeName(ctx context.Context, nodeID, name string) error {
	_ = ctx

	if nodeID == "" {
		return httputil.NewAppError(http.StatusBadRequest, "node id required")
	}
	if !s.repo.UpdateNodeName(nodeID, name) {
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

	for _, link := range s.linksForNode(nodeID) {
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
		if inspect.State.Paused {
			if err := s.docker.ContainerUnpause(ctx, node.ContainerID); err != nil {
				slog.Error("Failed to unfreeze node before start sync", "err", err)
				return httputil.NewAppError(http.StatusInternalServerError, "failed to unfreeze node before start sync")
			}
		}
		if err := s.syncNodeRuntime(ctx, nodeID, nil); err != nil {
			return err
		}
		s.repo.UpdateNodeStatus(nodeID, model.Running)
		s.resyncLinkedPeers(ctx, nodeID)
		return nil
	}

	if err := s.docker.ContainerStart(ctx, node.ContainerID, container.StartOptions{}); err != nil {
		slog.Error("Failed to start node", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "failed to start node")
	}

	if _, err := s.waitForNode(ctx, node); err != nil {
		return err
	}

	if err := s.syncNodeRuntime(ctx, nodeID, nil); err != nil {
		return err
	}

	s.repo.UpdateNodeStatus(nodeID, model.Running)
	s.resyncLinkedPeers(ctx, nodeID)
	return nil
}

func (s *Service) resyncLinkedPeers(ctx context.Context, nodeID string) {
	for _, peerID := range s.runningLinkedPeerIDs(nodeID) {
		if err := s.syncNodeRuntime(ctx, peerID, nil); err != nil {
			slog.Warn("Failed to re-sync peer after node start", "peer", peerID, "err", err)
		}
	}
}

func (s *Service) waitForNode(ctx context.Context, node model.Node) (int, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
		if err != nil {
			if client.IsErrNotFound(err) {
				return 0, httputil.NewAppError(http.StatusNotFound, "container not found")
			}
			slog.Error("Container inspect failed", "err", err)
			return 0, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
		}
		if inspect.State != nil && inspect.State.Running {
			return inspect.State.Pid, nil
		}
		if time.Now().After(deadline) {
			slog.Warn("Container not running after deadline", "container_id", node.ContainerID)
			return 0, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// syncNodeRuntime applies all in-container configuration after a node starts.
// prebuilt, when non-nil, is a set of interface IDs whose veths were already
// created by precreateVeths; ensureLinkedVeths is skipped in that case.
func (s *Service) syncNodeRuntime(ctx context.Context, nodeID string, prebuilt map[string]bool) error {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	var vethReady map[string]bool
	if prebuilt != nil {
		vethReady = prebuilt
	} else {
		// Single-node start: create veths lazily.
		vethReady = s.ensureLinkedVeths(ctx, node)
	}

	cmds := []string{"set -e"}

	// Build a map from interface ID to its kernel name.
	// Only include linked interfaces whose veth has been confirmed ready.
	ifaceExpr := make(map[string]string, len(node.Interfaces))
	for _, iface := range node.Interfaces {
		if iface.RuntimeName != "" {
			if iface.LinkID == "" || vethReady[iface.ID] {
				ifaceExpr[iface.ID] = iface.RuntimeName
			}
		}
	}

	varIdx := 0
	for _, iface := range node.Interfaces {
		if _, ok := ifaceExpr[iface.ID]; ok {
			continue
		}
		if iface.LinkID != "" {
			// Veth not ready (peer container stopped); skip — will be configured when peer starts.
			continue
		}
		if iface.RuntimeIPAddr == "" || iface.RuntimePrefixLen == 0 {
			continue
		}
		if varIdx == 0 {
			cmds = append(cmds,
				`_a=$(ip -o addr show)`,
				`_iface() { printf '%s\n' "$_a" | awk -v ip="$1" '$4==ip{print $2;exit}'; }`,
			)
		}
		varName := fmt.Sprintf("_e%d", varIdx)
		varIdx++
		cidr := fmt.Sprintf("%s/%d", iface.RuntimeIPAddr, iface.RuntimePrefixLen)
		cmds = append(cmds,
			fmt.Sprintf(`%s=$(_iface %q)`, varName, cidr),
			fmt.Sprintf(`[ -n "$%s" ] || { echo "name lookup failed: %s" >&2; exit 1; }`, varName, cidr),
			// Print for Go to capture and store in the repo.
			fmt.Sprintf(`echo "_rname_ %s $%s"`, iface.ID, varName),
		)
		ifaceExpr[iface.ID] = "$" + varName
	}

	if node.Type == model.Switch {
		cmds = append(cmds,
			"ip link show br0 >/dev/null 2>&1 || ip link add br0 type bridge",
			"ip link set br0 up",
		)
		for _, iface := range node.Interfaces {
			if iface.LinkID == "" {
				continue
			}
			expr, ok := ifaceExpr[iface.ID]
			if !ok {
				continue
			}
			cmds = append(cmds,
				fmt.Sprintf("ip link set %s master br0", expr),
				fmt.Sprintf("ip link set %s up", expr),
			)
		}
	}

	// Bring non-AdminDown interfaces up before assigning IPs and routes so that
	// gateway reachability checks in the kernel succeed.
	for _, iface := range node.Interfaces {
		if iface.AdminDown {
			continue
		}
		expr, hasExpr := ifaceExpr[iface.ID]
		if !hasExpr {
			continue
		}
		cmds = append(cmds, fmt.Sprintf("ip link set %s up", expr))
	}

	for _, iface := range node.Interfaces {
		expr, ok := ifaceExpr[iface.ID]
		if !ok {
			continue
		}
		ip, prefix := iface.IPAddr, iface.PrefixLen
		if ip == "" || prefix == 0 {
			ip, prefix = iface.RuntimeIPAddr, iface.RuntimePrefixLen
		}
		if ip == "" || prefix == 0 {
			continue
		}
		cmds = append(cmds, fmt.Sprintf("ip addr replace %s/%d dev %s", ip, prefix, expr))
	}

	// Build the set of active prefixes from interfaces that are confirmed in ifaceExpr.
	var activePrefixes []netip.Prefix
	for _, iface := range node.Interfaces {
		if _, ok := ifaceExpr[iface.ID]; !ok {
			continue
		}
		ip, prefix := iface.IPAddr, iface.PrefixLen
		if ip == "" || prefix == 0 {
			ip, prefix = iface.RuntimeIPAddr, iface.RuntimePrefixLen
		}
		if ip == "" || prefix == 0 {
			continue
		}
		if addr, err := netip.ParseAddr(ip); err == nil {
			if pfx, err := addr.Prefix(prefix); err == nil {
				activePrefixes = append(activePrefixes, pfx.Masked())
			}
		}
	}

	for _, route := range node.Routes {
		dest := route.Destination
		if dest == "0.0.0.0/0" {
			dest = "default"
		}
		switch route.Kind {
		case model.RouteKindVia:
			// Skip if the gateway isn't reachable via any active interface.
			gwReachable := false
			if gw, err := netip.ParseAddr(route.NextHop); err == nil {
				for _, pfx := range activePrefixes {
					if pfx.Contains(gw) {
						gwReachable = true
						break
					}
				}
			}
			if !gwReachable {
				continue
			}
			cmds = append(cmds, fmt.Sprintf("ip route replace %s via %s", dest, route.NextHop))
		case model.RouteKindBlackhole:
			cmds = append(cmds, fmt.Sprintf("ip route replace blackhole %s", dest))
		default:
			return httputil.NewAppError(http.StatusBadRequest, "invalid route kind")
		}
	}

	// Apply AdminDown state last.
	for _, iface := range node.Interfaces {
		if !iface.AdminDown {
			continue
		}
		expr, hasExpr := ifaceExpr[iface.ID]
		if !hasExpr {
			continue
		}
		cmds = append(cmds, fmt.Sprintf("ip link set %s down", expr))
	}

	var stdout string
	if len(cmds) > 1 {
		var err error
		stdout, err = execInContainerChecked(ctx, s.docker, node.ContainerID,
			[]string{"sh", "-c", strings.Join(cmds, "\n")},
			"failed to apply node runtime configuration")
		if err != nil {
			return err
		}
	}

	// Parse _rname_ lines emitted by the script to persist resolved interface names.
	if varIdx > 0 {
		for _, line := range strings.Split(stdout, "\n") {
			if !strings.HasPrefix(line, "_rname_ ") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) != 3 {
				continue
			}
			if !s.repo.UpdateInterfaceRuntimeName(nodeID, parts[1], parts[2]) {
				slog.Error("Failed to persist runtime interface name", "iface", parts[1])
				return httputil.NewAppError(http.StatusInternalServerError, "failed to persist runtime interface name")
			}
		}
		// Re-read so conditions/flap see the newly stored names.
		node, ok = s.repo.GetNode(nodeID)
		if !ok {
			return httputil.NewAppError(http.StatusNotFound, "node not found")
		}
	}

	for _, iface := range node.Interfaces {
		if !hasTrafficNetemConditions(iface.Conditions) && iface.Conditions.BandwidthKbit == 0 {
			continue
		}
		if err := s.applyRuntimeInterfaceConditions(ctx, node, iface); err != nil {
			return err
		}
	}

	for _, iface := range node.Interfaces {
		if iface.Flap.Enabled {
			if err := s.startRuntimeInterfaceFlap(ctx, node, iface); err != nil {
				return err
			}
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
	if inspect.State != nil && inspect.State.Paused {
		if err := s.docker.ContainerUnpause(ctx, node.ContainerID); err != nil {
			slog.Error("Failed to unfreeze node before stop", "err", err)
			return httputil.NewAppError(http.StatusInternalServerError, "failed to unfreeze node before stop")
		}
	}

	if err := s.docker.ContainerStop(ctx, node.ContainerID, container.StopOptions{}); err != nil {
		slog.Error("Failed to stop node", "err", err)
		return httputil.NewAppError(http.StatusInternalServerError, "failed to stop node: "+err.Error())
	}

	s.repo.UpdateNodeStatus(nodeID, model.Idle)
	return nil
}

func (s *Service) ToggleAllNodes(ctx context.Context) (string, error) {
	nodes, err := s.getNodes()
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "none", nil
	}

	startAll := false
	for _, node := range nodes {
		if node.Status != model.Running && node.Status != model.Frozen {
			startAll = true
			break
		}
	}

	if startAll {
		if err := s.startAllNodes(ctx, nodes); err != nil {
			return "", err
		}
		return "start", nil
	}

	if err := s.runNodeOpsParallel(ctx, nodes, s.stopNode); err != nil {
		return "", err
	}
	return "stop", nil
}

func (s *Service) runNodeOpsParallel(ctx context.Context, nodes []model.Node, operation func(context.Context, string) error) error {
	const workers = 16

	nodesCopy := make([]model.Node, len(nodes))
	copy(nodesCopy, nodes)
	sort.Slice(nodesCopy, func(i, j int) bool { return nodesCopy[i].ID < nodesCopy[j].ID })

	type task struct {
		index int
		node  model.Node
	}

	taskCh := make(chan task)
	errs := make([]error, len(nodesCopy))

	workerCount := workers
	if len(nodesCopy) < workerCount {
		workerCount = len(nodesCopy)
	}
	if workerCount == 0 {
		return nil
	}

	var wg sync.WaitGroup
	wg.Add(workerCount)
	for range workerCount {
		go func() {
			defer wg.Done()
			for item := range taskCh {
				if err := operation(ctx, item.node.ID); err != nil {
					errs[item.index] = err
				}
			}
		}()
	}

	for i, node := range nodesCopy {
		taskCh <- task{index: i, node: node}
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

func (s *Service) startAllNodes(ctx context.Context, nodes []model.Node) error {
	type nodeState struct {
		node           model.Node
		alreadyRunning bool
		pid            int
		err            error
	}

	states := make([]nodeState, len(nodes))
	for i, node := range nodes {
		states[i].node = node
	}

	// Phase 1: inspect and start all containers in parallel.
	var wg sync.WaitGroup
	wg.Add(len(states))
	for i := range states {
		go func() {
			defer wg.Done()
			node := states[i].node
			inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
			if err != nil {
				if client.IsErrNotFound(err) {
					states[i].err = httputil.NewAppError(http.StatusNotFound, "container not found")
				} else {
					slog.Error("Container inspect failed", "err", err)
					states[i].err = httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
				}
				return
			}
			if inspect.State != nil && inspect.State.Running {
				if inspect.State.Paused {
					if err := s.docker.ContainerUnpause(ctx, node.ContainerID); err != nil {
						slog.Error("Failed to unfreeze node", "err", err)
						states[i].err = httputil.NewAppError(http.StatusInternalServerError, "failed to unfreeze node")
						return
					}
				}
				states[i].alreadyRunning = true
				states[i].pid = inspect.State.Pid
				return
			}
			if err := s.docker.ContainerStart(ctx, node.ContainerID, container.StartOptions{}); err != nil {
				slog.Error("Failed to start node", "err", err)
				states[i].err = httputil.NewAppError(http.StatusInternalServerError, "failed to start node")
				return
			}
			slog.Info("Container start fired", "node_id", node.ID, "name", node.Name)
		}()
	}
	wg.Wait()

	// Phase 2A: wait for all containers to reach running state in parallel,
	// capturing each container's PID from the final inspect.
	var wg2a sync.WaitGroup
	wg2a.Add(len(states))
	for i := range states {
		go func() {
			defer wg2a.Done()
			if states[i].err != nil {
				return
			}
			if !states[i].alreadyRunning {
				pid, err := s.waitForNode(ctx, states[i].node)
				if err != nil {
					states[i].err = err
					return
				}
				states[i].pid = pid
			}
		}()
	}
	wg2a.Wait()

	// Phase 2B: create all veth pairs upfront using container PIDs already
	// captured in phases 1 and 2A — no extra ContainerInspect calls needed.
	pids := make(map[string]int, len(states))
	for _, st := range states {
		if st.err == nil && st.pid != 0 {
			pids[st.node.ContainerID] = st.pid
		}
	}
	prebuilt := s.precreateVeths(ctx, pids)

	// Phase 2C: sync all node runtimes in parallel.
	errs := make([]error, len(states))
	var wg2c sync.WaitGroup
	wg2c.Add(len(states))
	for i := range states {
		go func() {
			defer wg2c.Done()
			if states[i].err != nil {
				errs[i] = states[i].err
				return
			}
			node := states[i].node
			slog.Info("Container ready", "node_id", node.ID, "name", node.Name)
			if err := s.syncNodeRuntime(ctx, node.ID, prebuilt); err != nil {
				errs[i] = err
				return
			}
			slog.Info("Node runtime synced", "node_id", node.ID, "name", node.Name)
			s.repo.UpdateNodeStatus(node.ID, model.Running)
		}()
	}
	wg2c.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// precreateVeths creates every link's veth pair in parallel using container PIDs
// already captured during container startup. Returns a set of interface IDs
// whose veths are confirmed ready, allowing syncNodeRuntime to skip
// ensureLinkedVeths entirely for the start-all path.
func (s *Service) precreateVeths(ctx context.Context, pids map[string]int) map[string]bool {
	// Snapshot links and interface→container mapping.
	s.repo.store.Mu.RLock()
	linksCopy := make([]model.Link, 0, len(s.repo.store.Links))
	for _, l := range s.repo.store.Links {
		linksCopy = append(linksCopy, l)
	}
	ifaceContainer := make(map[string]string, len(s.repo.store.InterfaceOwnerIndex))
	for ifaceID, nodeID := range s.repo.store.InterfaceOwnerIndex {
		if node, ok := s.repo.store.Nodes[nodeID]; ok {
			ifaceContainer[ifaceID] = node.ContainerID
		}
	}
	s.repo.store.Mu.RUnlock()

	// Create all veth pairs in parallel — each link is created exactly once.
	prebuilt := make(map[string]bool)
	var mu2 sync.Mutex
	var wg2 sync.WaitGroup
	for _, link := range linksCopy {
		wg2.Add(1)
		go func(l model.Link) {
			defer wg2.Done()
			pidA := pids[ifaceContainer[l.InterfaceAID]]
			pidB := pids[ifaceContainer[l.InterfaceBID]]
			if pidA == 0 || pidB == 0 {
				return
			}
			if err := links.CreateVethPair(links.VethNameA(l.ID), links.VethNameB(l.ID), pidA, pidB); err != nil {
				slog.Warn("precreateVeths: failed", "link", l.ID, "err", err)
				return
			}
			mu2.Lock()
			prebuilt[l.InterfaceAID] = true
			prebuilt[l.InterfaceBID] = true
			mu2.Unlock()
		}(link)
	}
	wg2.Wait()

	return prebuilt
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
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = strings.TrimSpace(stdout)
		}
		if message == "" {
			message = failureMessage
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
	fields := strings.Fields(command)

	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	if command == "help" {
		return runHelp(command, node.Type), nil
	}

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		slog.Error("Container inspect failed", "err", err)
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	if command == "freeze" {
		return s.runFreeze(ctx, command, node, inspect)
	}
	if command == "unfreeze" {
		return s.runUnfreeze(ctx, command, node, inspect)
	}

	if inspect.State == nil || !inspect.State.Running {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "node is not running")
	}
	if inspect.State.Paused {
		s.repo.UpdateNodeStatus(nodeID, model.Frozen)
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "node is frozen")
	}

	if command == "ip addr" {
		if node.Type == model.Switch {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support ip addr")
		}
		return s.runIPAddr(ctx, command, node), nil
	}

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
		if len(fields) >= 2 && fields[0] == "ip" && fields[1] == "addr" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support ip addr")
		}
		if len(fields) >= 2 && fields[0] == "ip" && fields[1] == "set" && !(len(fields) == 5 && fields[2] == "link") {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch ports do not support ip assignment")
		}
		if len(fields) >= 2 && fields[0] == "ip" && fields[1] == "route" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch does not support routing commands")
		}
	}
	if len(fields) == 2 && fields[0] == "ping" {
		return s.runPing(ctx, command, node)
	}
	if len(fields) == 4 && fields[0] == "ping" && fields[2] == "--count" {
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
	if len(fields) == 5 && fields[0] == "ip" && fields[1] == "link" && fields[2] == "set" {
		return s.runIPLinkSet(ctx, command, nodeID, fields[3], fields[4])
	}
	if len(fields) >= 4 && fields[0] == "ip" && fields[1] == "flap" && fields[2] == "start" {
		return s.runIPFlapStart(ctx, command, nodeID, fields[3], fields[4:])
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "flap" && fields[2] == "stop" {
		return s.runIPFlapStop(ctx, command, nodeID, fields[3])
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "flap" && fields[2] == "status" {
		return s.runIPFlapStatus(ctx, command, nodeID, fields[3])
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "set" {
		return s.runIPSet(ctx, command, nodeID, fields[2], fields[3])
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "route" && fields[2] == "delete" {
		return s.runIPRouteDelete(ctx, command, node, fields[3])
	}
	if len(fields) == 4 && fields[0] == "ip" && fields[1] == "route" && fields[2] == "blackhole" {
		return s.runIPRouteBlackhole(ctx, command, node, fields[3])
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
	case "freeze":
		return "freeze", true
	case "unfreeze":
		return "unfreeze", true
	case "ip":
		if nodeType == model.Switch && len(fields) > 1 && fields[1] != "link" && fields[1] != "flap" {
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
		return "ping [target-ip] [--count packets]", true
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
		"ip link set [interface] [up|down]",
		"ip flap start [interface] [--down ms] [--up ms] [--jitter ms]",
		"ip flap stop [interface]",
		"ip flap status [interface]",
		"ip unset [interface]",
		"ip route",
		"ip route default [next-hop]",
		"ip route add [destination/prefix] via [next-hop]",
		"ip route blackhole [destination/prefix]",
		"ip route delete [default|destination/prefix]",
	}
	ipRouteCommands := []string{
		"ip route",
		"ip route default [next-hop]",
		"ip route add [destination/prefix] via [next-hop]",
		"ip route blackhole [destination/prefix]",
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
	case "link":
		return "ip link set [interface] [up|down]", true
	case "flap":
		if len(fields) == 2 {
			return strings.Join([]string{
				"ip flap start [interface] [--down ms] [--up ms] [--jitter ms]",
				"ip flap stop [interface]",
				"ip flap status [interface]",
			}, "\n"), true
		}
		switch fields[2] {
		case "start":
			return "ip flap start [interface] [--down ms] [--up ms] [--jitter ms]", true
		case "stop":
			return "ip flap stop [interface]", true
		case "status":
			return "ip flap status [interface]", true
		default:
			return strings.Join([]string{
				"ip flap start [interface] [--down ms] [--up ms] [--jitter ms]",
				"ip flap stop [interface]",
				"ip flap status [interface]",
			}, "\n"), true
		}
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
		case "blackhole":
			return "ip route blackhole [destination/prefix]", true
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
		"tc set [interface] [--delay ms] [--jitter ms] [--loss pct] [--loss-correlation pct] [--reorder pct] [--duplicate pct] [--corrupt pct] [--bandwidth kbit] [--queue-limit packets]",
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
		return "tc set [interface] [--delay ms] [--jitter ms] [--loss pct] [--loss-correlation pct] [--reorder pct] [--duplicate pct] [--corrupt pct] [--bandwidth kbit] [--queue-limit packets]", true
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
		"freeze",
		"unfreeze",
		"ip link set [interface] [up|down]",
		"ip flap start [interface] [--down ms] [--up ms] [--jitter ms]",
		"ip flap stop [interface]",
		"ip flap status [interface]",
	}

	if nodeType != model.Switch {
		lines = append(lines,
			"ip addr",
			"ip set [interface] [ip/prefix]",
			"ip unset [interface]",
			"ip route",
			"ip route default [next-hop]",
			"ip route add [destination/prefix] via [next-hop]",
			"ip route blackhole [destination/prefix]",
			"ip route delete [default|destination/prefix]",
			"ping [target-ip] [--count packets]",
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
		"tc set [interface] [--delay ms] [--jitter ms] [--loss pct] [--loss-correlation pct] [--reorder pct] [--duplicate pct] [--corrupt pct] [--bandwidth kbit] [--queue-limit packets]",
	)

	return commandResponse{
		Command:  command,
		Stdout:   strings.Join(lines, "\n"),
		Stderr:   "",
		ExitCode: 0,
	}
}

func (s *Service) runIPAddr(ctx context.Context, command string, node model.Node) commandResponse {
	lines := make([]string, 0, len(node.Interfaces))
	isRuntimeUpdatable := node.ContainerID != ""
	for _, iface := range node.Interfaces {
		state := "UP"
		if iface.AdminDown {
			state = "DOWN"
		}
		if isRuntimeUpdatable {
			if runtimeState, err := s.runtimeInterfaceState(ctx, node, iface); err == nil {
				if runtimeState == "up" {
					state = "UP"
				} else {
					state = "DOWN"
				}
			}
		}

		if iface.IPAddr == "" || iface.PrefixLen == 0 {
			lines = append(lines, fmt.Sprintf("%s: unassigned %s", iface.Name, state))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s/%d %s", iface.Name, iface.IPAddr, iface.PrefixLen, state))
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
	packetCount := 4
	if len(fields) != 2 {
		if len(fields) != 4 || fields[2] != "--count" {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid ping syntax")
		}
		parsedCount, err := strconv.Atoi(fields[3])
		if err != nil || parsedCount < 1 {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "packet count must be a positive integer")
		}
		packetCount = parsedCount
	}

	return s.execCommand(
		ctx,
		node.ContainerID,
		[]string{"ping", "-c", strconv.Itoa(packetCount), targetLogicalIP},
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
	if err := validateIperfUDPSyncDuration(fields[3:]); err != nil {
		return commandResponse{}, err
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

func validateIperfUDPSyncDuration(args []string) error {
	var bytesLimit uint64
	var hasBytes bool
	bitrateBps := 1_000_000.0
	for index := 0; index < len(args); index++ {
		if !strings.HasPrefix(args[index], "--") {
			return httputil.NewAppError(http.StatusBadRequest, "invalid iperf syntax")
		}
		flagName := args[index]
		if index+1 >= len(args) {
			return httputil.NewAppError(http.StatusBadRequest, "missing iperf flag value for "+flagName)
		}
		value := args[index+1]
		switch flagName {
		case "--bytes":
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil || parsed == 0 {
				return httputil.NewAppError(http.StatusBadRequest, "bytes must be a positive integer")
			}
			bytesLimit = parsed
			hasBytes = true
		case "--bitrate":
			parsed, err := parseIperfBitrateBps(value)
			if err != nil {
				return err
			}
			bitrateBps = parsed
		}
		index++
	}
	if !hasBytes || bitrateBps <= 0 {
		return nil
	}
	estimatedSeconds := float64(bytesLimit*8) / bitrateBps
	if estimatedSeconds <= maxSyncCommandDurationSeconds {
		return nil
	}
	return httputil.NewAppError(
		http.StatusBadRequest,
		fmt.Sprintf(
			"estimated runtime %.0fs exceeds %ds synchronous limit; use --time <= %d or lower --bytes",
			math.Ceil(estimatedSeconds),
			maxSyncCommandDurationSeconds,
			maxSyncCommandDurationSeconds,
		),
	)
}

func parseIperfBitrateBps(value string) (float64, error) {
	if !iperfBitratePattern.MatchString(value) {
		return 0, httputil.NewAppError(http.StatusBadRequest, "bitrate must be a positive number with optional K, M, or G suffix")
	}
	suffix := value[len(value)-1]
	multiplier := 1.0
	numberPart := value
	switch suffix {
	case 'k', 'K':
		numberPart = value[:len(value)-1]
		multiplier = 1_000
	case 'm', 'M':
		numberPart = value[:len(value)-1]
		multiplier = 1_000_000
	case 'g', 'G':
		numberPart = value[:len(value)-1]
		multiplier = 1_000_000_000
	}
	magnitude, err := strconv.ParseFloat(numberPart, 64)
	if err != nil || magnitude <= 0 {
		return 0, httputil.NewAppError(http.StatusBadRequest, "bitrate must be a positive number with optional K, M, or G suffix")
	}
	return magnitude * multiplier, nil
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
		[]string{"sh", "-c", fmt.Sprintf("mkdir -p /var/run && setsid sh -c 'echo $$ >%s; while true; do nc -l -p %d >/dev/null 2>&1; done' >/dev/null 2>&1 &", tcpPIDFilePath, port)},
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
		[]string{"sh", "-c", fmt.Sprintf("if [ -f %s ]; then kill -TERM -\"$(cat %s)\" >/dev/null 2>&1 || true; fi; rm -f %s", tcpPIDFilePath, tcpPIDFilePath, tcpPIDFilePath)},
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

func (s *Service) runFreeze(ctx context.Context, command string, node model.Node, inspect types.ContainerJSON) (commandResponse, error) {
	if inspect.State == nil || !inspect.State.Running {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "node is not running")
	}
	if inspect.State.Paused {
		s.repo.UpdateNodeStatus(node.ID, model.Frozen)
		return commandResponse{Command: command, Stdout: "node already frozen", Stderr: "", ExitCode: 0}, nil
	}

	if err := s.docker.ContainerPause(ctx, node.ContainerID); err != nil {
		slog.Error("Failed to freeze node", "err", err)
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "failed to freeze node")
	}
	s.repo.UpdateNodeStatus(node.ID, model.Frozen)

	return commandResponse{Command: command, Stdout: "node frozen", Stderr: "", ExitCode: 0}, nil
}

func (s *Service) runUnfreeze(ctx context.Context, command string, node model.Node, inspect types.ContainerJSON) (commandResponse, error) {
	if inspect.State == nil || !inspect.State.Running {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "node is not running")
	}
	if !inspect.State.Paused {
		s.repo.UpdateNodeStatus(node.ID, model.Running)
		return commandResponse{Command: command, Stdout: "node already running", Stderr: "", ExitCode: 0}, nil
	}

	if err := s.docker.ContainerUnpause(ctx, node.ContainerID); err != nil {
		slog.Error("Failed to unfreeze node", "err", err)
		return commandResponse{}, httputil.NewAppError(http.StatusInternalServerError, "failed to unfreeze node")
	}
	s.repo.UpdateNodeStatus(node.ID, model.Running)

	return commandResponse{Command: command, Stdout: "node unfrozen", Stderr: "", ExitCode: 0}, nil
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
	if inspect.State != nil && inspect.State.Running && targetIface.LinkID != "" && targetIface.RuntimeName != "" {
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
	if inspect.State != nil && inspect.State.Running && targetIface.LinkID != "" && targetIface.RuntimeName != "" {
		if err := s.clearRuntimeInterfaceConditions(ctx, node.ContainerID, targetIface.RuntimeName); err != nil {
			_ = s.repo.UpdateInterfaceConditions(nodeID, interfaceName, previousConditions)
			targetIface.Conditions = previousConditions
			_ = s.applyRuntimeInterfaceConditions(ctx, node, targetIface)
			return commandResponse{}, err
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
		"%s: delay=%dms jitter=%dms loss=%s%% loss-correlation=%s%% reorder=%s%% duplicate=%s%% corrupt=%s%% bandwidth=%dkbit queue-limit=%d",
		iface.Name,
		iface.Conditions.DelayMs,
		iface.Conditions.JitterMs,
		strconv.FormatFloat(iface.Conditions.LossPct, 'f', -1, 64),
		strconv.FormatFloat(iface.Conditions.LossCorrelationPct, 'f', -1, 64),
		strconv.FormatFloat(iface.Conditions.ReorderPct, 'f', -1, 64),
		strconv.FormatFloat(iface.Conditions.DuplicatePct, 'f', -1, 64),
		strconv.FormatFloat(iface.Conditions.CorruptPct, 'f', -1, 64),
		iface.Conditions.BandwidthKbit,
		iface.Conditions.QueueLimitPackets,
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
	if math.IsNaN(conditions.LossCorrelationPct) || math.IsInf(conditions.LossCorrelationPct, 0) {
		return httputil.NewAppError(http.StatusBadRequest, "loss correlation must be finite")
	}
	if conditions.LossCorrelationPct < 0 || conditions.LossCorrelationPct > 100 {
		return httputil.NewAppError(http.StatusBadRequest, "loss correlation must be between 0 and 100")
	}
	if conditions.LossPct == 0 && conditions.LossCorrelationPct > 0 {
		return httputil.NewAppError(http.StatusBadRequest, "loss correlation requires loss")
	}
	if math.IsNaN(conditions.ReorderPct) || math.IsInf(conditions.ReorderPct, 0) {
		return httputil.NewAppError(http.StatusBadRequest, "reorder must be finite")
	}
	if conditions.ReorderPct < 0 || conditions.ReorderPct > 100 {
		return httputil.NewAppError(http.StatusBadRequest, "reorder must be between 0 and 100")
	}
	if conditions.ReorderPct > 0 && conditions.DelayMs == 0 {
		return httputil.NewAppError(http.StatusBadRequest, "reorder requires delay")
	}
	if math.IsNaN(conditions.DuplicatePct) || math.IsInf(conditions.DuplicatePct, 0) {
		return httputil.NewAppError(http.StatusBadRequest, "duplicate must be finite")
	}
	if conditions.DuplicatePct < 0 || conditions.DuplicatePct > 100 {
		return httputil.NewAppError(http.StatusBadRequest, "duplicate must be between 0 and 100")
	}
	if math.IsNaN(conditions.CorruptPct) || math.IsInf(conditions.CorruptPct, 0) {
		return httputil.NewAppError(http.StatusBadRequest, "corrupt must be finite")
	}
	if conditions.CorruptPct < 0 || conditions.CorruptPct > 100 {
		return httputil.NewAppError(http.StatusBadRequest, "corrupt must be between 0 and 100")
	}
	if conditions.BandwidthKbit < 0 {
		return httputil.NewAppError(http.StatusBadRequest, "bandwidth must be non-negative")
	}
	if conditions.QueueLimitPackets < 0 {
		return httputil.NewAppError(http.StatusBadRequest, "queue limit must be non-negative")
	}

	return nil
}

func hasTrafficNetemConditions(conditions model.TrafficConditions) bool {
	return conditions.DelayMs > 0 || conditions.JitterMs > 0 || conditions.LossPct > 0 || conditions.ReorderPct > 0 || conditions.DuplicatePct > 0 || conditions.CorruptPct > 0 || conditions.QueueLimitPackets > 0
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
	if conditions.ReorderPct > 0 {
		args = append(args, "reorder", strconv.FormatFloat(conditions.ReorderPct, 'f', -1, 64)+"%")
	}
	if conditions.DuplicatePct > 0 {
		args = append(args, "duplicate", strconv.FormatFloat(conditions.DuplicatePct, 'f', -1, 64)+"%")
	}
	if conditions.CorruptPct > 0 {
		args = append(args, "corrupt", strconv.FormatFloat(conditions.CorruptPct, 'f', -1, 64)+"%")
	}
	if conditions.QueueLimitPackets > 0 {
		args = append(args, "limit", strconv.Itoa(conditions.QueueLimitPackets))
	}

	return args
}

func parseTCSetConditions(args []string, current model.TrafficConditions) (model.TrafficConditions, error) {
	if len(args) == 0 {
		return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "tc set requires at least one flag")
	}

	conditions := current
	seen := make(map[string]struct{}, 10)

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
		case "--loss-correlation":
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid loss correlation value")
			}
			conditions.LossCorrelationPct = parsed
		case "--bandwidth":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid bandwidth value")
			}
			conditions.BandwidthKbit = parsed
		case "--queue-limit":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid queue limit value")
			}
			conditions.QueueLimitPackets = parsed
		case "--reorder":
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid reorder value")
			}
			conditions.ReorderPct = parsed
		case "--duplicate":
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid duplicate value")
			}
			conditions.DuplicatePct = parsed
		case "--corrupt":
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return model.TrafficConditions{}, httputil.NewAppError(http.StatusBadRequest, "invalid corrupt value")
			}
			conditions.CorruptPct = parsed
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

	if s.repo.store.LinkSubnets.IsReserved(prefix) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "address overlaps the reserved management range")
	}

	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return commandResponse{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}
	if node.Type == model.Switch {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch ports do not support ip assignment")
	}

	if !s.repo.UpdateInterfaceAddress(nodeID, interfaceName, prefix.Addr().String(), prefix.Bits()) {
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
			return commandResponse{
				Command:  command,
				Stdout:   fmt.Sprintf("%s set to %s", interfaceName, cidr),
				Stderr:   "",
				ExitCode: 0,
			}, nil
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
	if node.Type == model.Switch {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "switch ports do not support ip assignment")
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

	if inspect.State != nil && inspect.State.Running && targetIface.LinkID != "" && targetIface.RuntimeName != "" {
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

func (s *Service) runIPLinkSet(ctx context.Context, command, nodeID, interfaceName, state string) (commandResponse, error) {
	if state != "up" && state != "down" {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "link state must be up or down")
	}

	node, iface, inspect, err := s.loadInterfaceForRuntimeCommand(ctx, nodeID, interfaceName)
	if err != nil {
		return commandResponse{}, err
	}

	adminDown := state == "down"
	if !s.repo.UpdateInterfaceAdminDown(nodeID, interfaceName, adminDown) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}
	if !s.repo.UpdateInterfaceFlap(nodeID, interfaceName, model.InterfaceFlap{}) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}

	if inspect.State != nil && inspect.State.Running {
		if err := s.stopRuntimeInterfaceFlap(ctx, node, iface); err != nil {
			return commandResponse{}, err
		}
		if err := s.applyRuntimeInterfaceLinkState(ctx, node, iface, adminDown); err != nil {
			return commandResponse{}, err
		}
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("%s set %s", interfaceName, state),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) runIPFlapStart(ctx context.Context, command, nodeID, interfaceName string, args []string) (commandResponse, error) {
	downMs := defaultFlapDownMs
	upMs := defaultFlapUpMs
	jitterMs := defaultFlapJitterMs

	for index := 0; index < len(args); {
		if index+1 >= len(args) {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "missing ip flap flag value")
		}
		flagName := args[index]
		value := args[index+1]
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid ip flap value")
		}
		switch flagName {
		case "--down":
			downMs = parsed
		case "--up":
			upMs = parsed
		case "--jitter":
			jitterMs = parsed
		default:
			return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "unsupported ip flap flag: "+flagName)
		}
		index += 2
	}
	if downMs == 0 && upMs == 0 {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "down and up durations cannot both be zero")
	}

	node, iface, inspect, err := s.loadInterfaceForRuntimeCommand(ctx, nodeID, interfaceName)
	if err != nil {
		return commandResponse{}, err
	}

	flap := model.InterfaceFlap{Enabled: true, DownMs: downMs, UpMs: upMs, JitterMs: jitterMs}
	if !s.repo.UpdateInterfaceAdminDown(nodeID, interfaceName, false) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}
	if !s.repo.UpdateInterfaceFlap(nodeID, interfaceName, flap) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}

	if inspect.State != nil && inspect.State.Running {
		if err := s.startRuntimeInterfaceFlap(ctx, node, iface); err != nil {
			return commandResponse{}, err
		}
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("%s flap enabled (down=%dms up=%dms jitter=%dms)", interfaceName, downMs, upMs, jitterMs),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) runIPFlapStop(ctx context.Context, command, nodeID, interfaceName string) (commandResponse, error) {
	node, iface, inspect, err := s.loadInterfaceForRuntimeCommand(ctx, nodeID, interfaceName)
	if err != nil {
		return commandResponse{}, err
	}

	if !s.repo.UpdateInterfaceFlap(nodeID, interfaceName, model.InterfaceFlap{}) {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}

	if inspect.State != nil && inspect.State.Running {
		if err := s.stopRuntimeInterfaceFlap(ctx, node, iface); err != nil {
			return commandResponse{}, err
		}
		if err := s.applyRuntimeInterfaceLinkState(ctx, node, iface, iface.AdminDown); err != nil {
			return commandResponse{}, err
		}
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("%s flap disabled", interfaceName),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) runIPFlapStatus(ctx context.Context, command, nodeID, interfaceName string) (commandResponse, error) {
	node, iface, inspect, err := s.loadInterfaceForRuntimeCommand(ctx, nodeID, interfaceName)
	if err != nil {
		return commandResponse{}, err
	}

	currentState := "unknown"
	if inspect.State != nil && inspect.State.Running {
		currentState, err = s.runtimeInterfaceState(ctx, node, iface)
		if err != nil {
			return commandResponse{}, err
		}
	}

	status := "stopped"
	if iface.Flap.Enabled {
		status = fmt.Sprintf("running down=%dms up=%dms jitter=%dms", iface.Flap.DownMs, iface.Flap.UpMs, iface.Flap.JitterMs)
	}

	return commandResponse{
		Command:  command,
		Stdout:   fmt.Sprintf("%s: state=%s admin-down=%t flap=%s", interfaceName, currentState, iface.AdminDown, status),
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (s *Service) loadInterfaceForRuntimeCommand(ctx context.Context, nodeID, interfaceName string) (model.Node, model.Interface, types.ContainerJSON, error) {
	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return model.Node{}, model.Interface{}, types.ContainerJSON{}, httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	var iface model.Interface
	found := false
	for _, candidate := range node.Interfaces {
		if candidate.Name != interfaceName {
			continue
		}
		iface = candidate
		found = true
		break
	}
	if !found {
		return model.Node{}, model.Interface{}, types.ContainerJSON{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
	}

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return model.Node{}, model.Interface{}, types.ContainerJSON{}, httputil.NewAppError(http.StatusNotFound, "container not found")
		}
		slog.Error("Container inspect failed", "err", err)
		return model.Node{}, model.Interface{}, types.ContainerJSON{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	if inspect.State != nil && inspect.State.Running {
		iface, err = s.ensureRuntimeInterfaceForCommand(ctx, node, inspect, iface.Name)
		if err != nil {
			return model.Node{}, model.Interface{}, types.ContainerJSON{}, err
		}
		updatedNode, ok := s.repo.GetNode(nodeID)
		if ok {
			node = updatedNode
		}
	}

	return node, iface, inspect, nil
}

func (s *Service) ensureRuntimeInterfaceForCommand(_ context.Context, node model.Node, _ types.ContainerJSON, interfaceName string) (model.Interface, error) {
	for _, iface := range node.Interfaces {
		if iface.Name == interfaceName {
			if iface.RuntimeName == "" {
				iface.RuntimeName = iface.Name
			}
			return iface, nil
		}
	}
	return model.Interface{}, httputil.NewAppError(http.StatusBadRequest, "interface not found on node")
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

func (s *Service) applyRuntimeInterfaceState(ctx context.Context, node model.Node, iface model.Interface) error {
	if err := s.applyRuntimeInterfaceLinkState(ctx, node, iface, iface.AdminDown); err != nil {
		return err
	}
	return nil
}

func (s *Service) applyRuntimeInterfaceLinkState(ctx context.Context, node model.Node, iface model.Interface, adminDown bool) error {
	runtimeName := iface.RuntimeName
	if runtimeName == "" {
		// Some interfaces (e.g., unconnected switch ports) do not exist in runtime yet.
		// Skip until a concrete runtime interface name is resolved.
		if iface.LinkID == "" {
			return nil
		}
		runtimeName = iface.Name
	}
	state := "up"
	if adminDown {
		state = "down"
	}
	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"ip", "link", "set", runtimeName, state},
		"failed to apply runtime interface link state",
	); err != nil {
		return err
	}
	return nil
}

func interfaceFlapPIDFilePath(iface model.Interface) string {
	return fmt.Sprintf("/var/run/ip-flap-%s.pid", iface.ID)
}

func buildInterfaceFlapCommand(iface model.Interface) string {
	runtimeName := iface.RuntimeName
	if runtimeName == "" {
		runtimeName = iface.Name
	}

	return fmt.Sprintf(`pidfile=%q; iface=%q; down_ms=%d; up_ms=%d; jitter_ms=%d;
rand_ms() {
  if [ "$1" -le 0 ]; then echo 0; return; fi
  n=$(od -An -N2 -tu2 /dev/urandom | tr -d ' ')
  echo $(( n %% ($1 + 1) ))
}
sleep_ms() {
  sleep "$(awk -v ms="$1" 'BEGIN { printf "%%.3f", ms / 1000.0 }')"
}
interval_ms() {
  base="$1"
  if [ "$jitter_ms" -le 0 ]; then echo "$base"; return; fi
  spread=$(( ( $(rand_ms $((jitter_ms * 2))) ) - jitter_ms ))
  value=$(( base + spread ))
  if [ "$value" -lt 0 ]; then value=0; fi
  echo "$value"
}
while true; do
  ip link set "$iface" down
  sleep_ms "$(interval_ms "$down_ms")"
  ip link set "$iface" up
  sleep_ms "$(interval_ms "$up_ms")"
done > /dev/null 2>&1 & echo $! > "$pidfile"`, interfaceFlapPIDFilePath(iface), runtimeName, iface.Flap.DownMs, iface.Flap.UpMs, iface.Flap.JitterMs)
}

func (s *Service) startRuntimeInterfaceFlap(ctx context.Context, node model.Node, iface model.Interface) error {
	if !iface.Flap.Enabled {
		return nil
	}
	if err := s.stopRuntimeInterfaceFlap(ctx, node, iface); err != nil {
		return err
	}
	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"sh", "-c", buildInterfaceFlapCommand(iface)},
		"failed to start interface flap",
	); err != nil {
		return err
	}
	return nil
}

func (s *Service) stopRuntimeInterfaceFlap(ctx context.Context, node model.Node, iface model.Interface) error {
	pidFilePath := interfaceFlapPIDFilePath(iface)
	if _, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"sh", "-c", fmt.Sprintf(`if [ -f %q ]; then kill "$(cat %q)" >/dev/null 2>&1 || true; rm -f %q; fi`, pidFilePath, pidFilePath, pidFilePath)},
		"failed to stop interface flap",
	); err != nil {
		return err
	}
	return nil
}

func (s *Service) runtimeInterfaceState(ctx context.Context, node model.Node, iface model.Interface) (string, error) {
	runtimeName := iface.RuntimeName
	if runtimeName == "" {
		runtimeName = iface.Name
	}
	stdout, err := execInContainerChecked(
		ctx,
		s.docker,
		node.ContainerID,
		[]string{"sh", "-c", fmt.Sprintf(`cat /sys/class/net/%s/operstate`, runtimeName)},
		"failed to inspect interface state",
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout), nil
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
			[]string{"tc", "qdisc", "replace", "dev", iface.RuntimeName, "root", "handle", "1:", "tbf", "rate", rate, "burst", "1600", "latency", "50ms"},
			"failed to apply tc bandwidth qdisc",
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
		if route.Kind == model.RouteKindBlackhole {
			lines = append(lines, fmt.Sprintf("blackhole %s", route.Destination))
			continue
		}
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
		Kind:        model.RouteKindVia,
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

func (s *Service) runIPRouteBlackhole(ctx context.Context, command string, node model.Node, destination string) (commandResponse, error) {
	prefix, err := netip.ParsePrefix(destination)
	if err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "invalid route destination")
	}

	route := model.Route{
		Destination: prefix.Masked().String(),
		Kind:        model.RouteKindBlackhole,
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
		Stdout:   fmt.Sprintf("blackhole route %s configured", route.Destination),
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

	var execCmd []string
	switch route.Kind {
	case model.RouteKindVia:
		execCmd = []string{"ip", "route", "del", destination}
		execCmd = append(execCmd, "via", route.NextHop)
	case model.RouteKindBlackhole:
		execCmd = []string{"ip", "route", "del", "blackhole", destination}
	default:
		return httputil.NewAppError(http.StatusBadRequest, "invalid route kind")
	}

	stdout, stderr, exitCode, err := execInContainer(ctx, s.docker, node.ContainerID, execCmd)
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

func (s *Service) ensureLinkedVeths(ctx context.Context, node model.Node) map[string]bool {
	ready := make(map[string]bool)

	inspect, err := s.docker.ContainerInspect(ctx, node.ContainerID)
	if err != nil || inspect.State == nil || !inspect.State.Running {
		return ready
	}
	pidThis := inspect.State.Pid
	if pidThis == 0 {
		return ready
	}

	s.repo.store.Mu.RLock()
	linksCopy := make(map[string]model.Link, len(s.repo.store.Links))
	for k, v := range s.repo.store.Links {
		linksCopy[k] = v
	}
	s.repo.store.Mu.RUnlock()

	for _, iface := range node.Interfaces {
		if iface.LinkID == "" || iface.RuntimeName == "" {
			continue
		}
		link, ok := linksCopy[iface.LinkID]
		if !ok {
			continue
		}

		thisIsA := link.InterfaceAID == iface.ID
		peerIfaceID := link.InterfaceBID
		if !thisIsA {
			peerIfaceID = link.InterfaceAID
		}

		s.repo.store.Mu.RLock()
		peerNodeID, ok := s.repo.store.InterfaceOwnerIndex[peerIfaceID]
		var peerContainerID string
		if ok {
			if peerNode, ok2 := s.repo.store.Nodes[peerNodeID]; ok2 {
				peerContainerID = peerNode.ContainerID
			}
		}
		s.repo.store.Mu.RUnlock()

		if peerContainerID == "" {
			continue
		}

		peerInspect, err := s.docker.ContainerInspect(ctx, peerContainerID)
		if err != nil || peerInspect.State == nil || !peerInspect.State.Running {
			continue
		}
		pidPeer := peerInspect.State.Pid
		if pidPeer == 0 {
			continue
		}

		var nameThis, namePeer string
		if thisIsA {
			nameThis, namePeer = links.VethNameA(link.ID), links.VethNameB(link.ID)
		} else {
			nameThis, namePeer = links.VethNameB(link.ID), links.VethNameA(link.ID)
		}

		vethOK := false
		if err := links.CreateVethPair(nameThis, namePeer, pidThis, pidPeer); err != nil {
			// Peer node may have created the pair simultaneously and is still
			// moving our end into this container. Retry a few times.
			for i := 0; i < 5; i++ {
				if i > 0 {
					time.Sleep(50 * time.Millisecond)
				}
				_, _, exitCode, _ := execInContainer(ctx, s.docker, node.ContainerID,
					[]string{"ip", "link", "show", nameThis})
				if exitCode == 0 {
					vethOK = true
					break
				}
			}
			if !vethOK {
				slog.Warn("ensureLinkedVeths: veth unavailable", "interface", nameThis, "err", err)
			}
		} else {
			vethOK = true
		}
		if vethOK {
			ready[iface.ID] = true
		}
	}
	return ready
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

// runningLinkedPeerIDs returns the IDs of distinct nodes that are currently
// running and share a link with the given node.
func (s *Service) runningLinkedPeerIDs(nodeID string) []string {
	s.repo.store.Mu.RLock()
	defer s.repo.store.Mu.RUnlock()

	node, ok := s.repo.store.Nodes[nodeID]
	if !ok {
		return nil
	}

	seen := make(map[string]bool)
	var peers []string
	for _, iface := range node.Interfaces {
		if iface.LinkID == "" {
			continue
		}
		link, ok := s.repo.store.Links[iface.LinkID]
		if !ok {
			continue
		}
		peerIfaceID := link.InterfaceBID
		if peerIfaceID == iface.ID {
			peerIfaceID = link.InterfaceAID
		}
		peerNodeID, ok := s.repo.store.InterfaceOwnerIndex[peerIfaceID]
		if !ok || peerNodeID == nodeID || seen[peerNodeID] {
			continue
		}
		peerNode, ok := s.repo.store.Nodes[peerNodeID]
		if !ok {
			continue
		}
		if peerNode.Status == model.Running || peerNode.Status == model.Frozen {
			seen[peerNodeID] = true
			peers = append(peers, peerNodeID)
		}
	}
	return peers
}
