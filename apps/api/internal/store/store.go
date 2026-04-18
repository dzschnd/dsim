package store

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

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

func NewStore() (*Store, error) {
	isolatedSubnets, err := NewSubnetAllocator("10.250.0.0/16", 30)
	if err != nil {
		return nil, err
	}
	linkSubnets, err := NewSubnetAllocator("10.251.0.0/16", 29)
	if err != nil {
		return nil, err
	}

	return &Store{
		Nodes:               make(map[string]model.Node),
		Links:               make(map[string]model.Link),
		LinkIndex:           make(map[string]string),
		InterfaceOwnerIndex: make(map[string]string),
		IsolatedSubnets:     isolatedSubnets,
		LinkSubnets:         linkSubnets,
	}, nil
}

func NewID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}
