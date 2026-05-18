package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/model"
)

type Store struct {
	Mu                  sync.RWMutex
	Nodes               map[string]model.Node
	Links               map[string]model.Link
	LinkIndex           map[string]string
	InterfaceOwnerIndex map[string]string
	LinkSubnets         *SubnetAllocator
	hostNameSeq         int
	switchNameSeq       int
	routerNameSeq       int
}

func NewStore(_ context.Context, _ *client.Client) (*Store, error) {
	linkSubnets, err := NewSubnetAllocator("10.251.0.0/16", 29)
	if err != nil {
		return nil, err
	}

	return &Store{
		Nodes:               make(map[string]model.Node),
		Links:               make(map[string]model.Link),
		LinkIndex:           make(map[string]string),
		InterfaceOwnerIndex: make(map[string]string),
		LinkSubnets:         linkSubnets,
	}, nil
}

func NewID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

func (s *Store) NodesSnapshot() []model.Node {
	s.Mu.RLock()
	defer s.Mu.RUnlock()

	nodes := make([]model.Node, 0, len(s.Nodes))
	for _, node := range s.Nodes {
		nodes = append(nodes, node)
	}

	return nodes
}

func (s *Store) LinksSnapshot() []model.Link {
	s.Mu.RLock()
	defer s.Mu.RUnlock()

	links := make([]model.Link, 0, len(s.Links))
	for _, link := range s.Links {
		links = append(links, link)
	}

	return links
}

func (s *Store) NextDefaultNodeName(nodeType model.NodeType) string {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	switch nodeType {
	case model.Host:
		s.hostNameSeq++
		return "h" + fmt.Sprintf("%d", s.hostNameSeq)
	case model.Switch:
		s.switchNameSeq++
		return "s" + fmt.Sprintf("%d", s.switchNameSeq)
	case model.Router:
		s.routerNameSeq++
		return "r" + fmt.Sprintf("%d", s.routerNameSeq)
	default:
		return ""
	}
}
