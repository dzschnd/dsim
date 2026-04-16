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
}

func NewStore() *Store {
	return &Store{
		Nodes:               make(map[string]model.Node),
		Links:               make(map[string]model.Link),
		LinkIndex:           make(map[string]string),
		InterfaceOwnerIndex: make(map[string]string),
	}
}

func NewID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}
