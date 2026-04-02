package nodes

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/dzschnd/dsim/internal/httputil"
	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

type service struct {
	docker *client.Client
	repo   *repository
}

func newService(docker *client.Client, s *store.Store) *service {
	return &service{docker: docker, repo: newRepository(s)}
}

func (s *service) checkStoreExists() error {
	if s.repo.store == nil {
		return httputil.NewAppError(http.StatusInternalServerError, "store not initialized")
	}
	return nil
}

func (s *service) getNodes() ([]model.Node, error) {
	if err := s.checkStoreExists(); err != nil {
		return nil, err
	}
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
	if err := s.checkStoreExists(); err != nil {
		return model.Node{}, err
	}

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

	initEnabled := true
	createResp, err := s.docker.ContainerCreate(
		ctx,
		&container.Config{Image: image},
		&container.HostConfig{Init: &initEnabled},
		nil, nil, "",
	)
	if err != nil {
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "container create failed")
	}

	inspect, err := s.docker.ContainerInspect(ctx, createResp.ID)
	if err != nil {
		return model.Node{}, httputil.NewAppError(http.StatusInternalServerError, "container inspect failed")
	}

	name := strings.TrimPrefix(inspect.Name, "/")
	nodeID := store.NewID("node_")
	node := model.Node{
		ID:          nodeID,
		Name:        name,
		Status:      model.Idle,
		Type:        nodeType,
		ContainerID: createResp.ID,
		CreatedAt:   time.Now().UTC(),
	}
	s.repo.AddNode(node)

	return node, nil
}

func (s *service) deleteNode(ctx context.Context, nodeID string) error {
	if err := s.checkStoreExists(); err != nil {
		return err
	}
	if nodeID == "" {
		return httputil.NewAppError(http.StatusBadRequest, "node id required")
	}

	node, ok := s.repo.GetNode(nodeID)
	if !ok {
		return httputil.NewAppError(http.StatusNotFound, "node not found")
	}

	links := s.repo.store.ListLinks()
	for _, link := range links {
		if link.NodeAID == nodeID || link.NodeBID == nodeID {
			s.repo.store.DeleteLink(link.ID)
		}
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
	if err := s.checkStoreExists(); err != nil {
		return err
	}
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
		s.repo.UpdateNodeStatus(nodeID, model.Running)
		return nil
	}

	if err := s.docker.ContainerStart(ctx, node.ContainerID, container.StartOptions{}); err != nil {
		return httputil.NewAppError(http.StatusInternalServerError, "failed to start node")
	}

	s.repo.UpdateNodeStatus(nodeID, model.Running)
	return nil
}

func (s *service) stopNode(ctx context.Context, nodeID string) error {
	if err := s.checkStoreExists(); err != nil {
		return err
	}
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

func parseCommand(command string) ([]string, error) {
	switch command {
	case "ip addr":
		return []string{"ip", "addr"}, nil
	}

	fields := strings.Fields(command)
	if len(fields) == 2 && fields[0] == "ping" {
		return []string{"ping", "-c", "4", fields[1]}, nil
	}

	return nil, errors.New("unsupported command: " + command)
}

func (s *service) runCommand(ctx context.Context, nodeID, command string) (commandResponse, error) {
	if err := s.checkStoreExists(); err != nil {
		return commandResponse{}, err
	}
	if nodeID == "" {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "node id required")
	}
	if strings.TrimSpace(command) == "" {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, "command is required")
	}

	execCmd, err := parseCommand(command)
	if err != nil {
		return commandResponse{}, httputil.NewAppError(http.StatusBadRequest, err.Error())
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

	execResp, err := s.docker.ContainerExecCreate(ctx, node.ContainerID, container.ExecOptions{
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
