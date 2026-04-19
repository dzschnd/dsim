package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/netip"
	"sync"

	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/model"
)

type Store struct {
	Mu                  sync.RWMutex
	Nodes               map[string]model.Node
	Links               map[string]model.Link
	LinkIndex           map[string]string
	InterfaceOwnerIndex map[string]string
	IsolatedSubnets     *SubnetAllocator
	LinkSubnets         *SubnetAllocator
}

func NewStore(ctx context.Context, docker *client.Client) (*Store, error) {
	isolatedSubnets, err := NewSubnetAllocator("10.250.0.0/16", 30)
	if err != nil {
		return nil, err
	}
	linkSubnets, err := NewSubnetAllocator("10.251.0.0/16", 29)
	if err != nil {
		return nil, err
	}

	s := &Store{
		Nodes:               make(map[string]model.Node),
		Links:               make(map[string]model.Link),
		LinkIndex:           make(map[string]string),
		InterfaceOwnerIndex: make(map[string]string),
		IsolatedSubnets:     isolatedSubnets,
		LinkSubnets:         linkSubnets,
	}

	if docker != nil {
		if err := hydrateSubnetAllocators(ctx, docker, s); err != nil {
			return nil, err
		}
	}

	return s, nil
}

func NewID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

func hydrateSubnetAllocators(ctx context.Context, docker *client.Client, s *Store) error {
	networks, err := docker.NetworkList(ctx, dockernetwork.ListOptions{})
	if err != nil {
		return err
	}

	for _, network := range networks {
		for _, config := range network.IPAM.Config {
			if config.Subnet == "" {
				continue
			}
			subnet, err := netip.ParsePrefix(config.Subnet)
			if err != nil {
				continue
			}
			s.IsolatedSubnets.ReserveOverlapping(subnet)
			s.LinkSubnets.ReserveOverlapping(subnet)
		}
	}

	return nil
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
