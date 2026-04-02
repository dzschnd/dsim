package store

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"

	"github.com/dzschnd/dsim/internal/model"
)

type Store struct {
	Mu        sync.RWMutex
	Nodes     map[string]model.Node
	links     map[string]model.Link
	linkIndex map[string]string
}

func NewStore() *Store {
	return &Store{
		Nodes:     make(map[string]model.Node),
		links:     make(map[string]model.Link),
		linkIndex: make(map[string]string),
	}
}

func (s *Store) AddLink(link model.Link) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.links[link.ID] = link
	s.linkIndex[linkKey(link.NodeAID, link.NodeBID)] = link.ID
}

func (s *Store) HasLink(nodeAID, nodeBID string) bool {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	_, ok := s.linkIndex[linkKey(nodeAID, nodeBID)]
	return ok
}

func (s *Store) GetLink(id string) (model.Link, bool) {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	link, ok := s.links[id]
	return link, ok
}

func (s *Store) DeleteLink(id string) bool {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	link, ok := s.links[id]
	if !ok {
		return false
	}
	delete(s.links, id)
	delete(s.linkIndex, linkKey(link.NodeAID, link.NodeBID))
	return true
}

func (s *Store) ListLinks() []model.Link {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	out := make([]model.Link, 0, len(s.links))
	for _, link := range s.links {
		out = append(out, link)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func NewID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

func linkKey(a, b string) string {
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}
