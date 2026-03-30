package routes

import (
	"crypto/rand"
	"encoding/hex"
	"github.com/dzschnd/dsim/internal/model"
	"sort"
	"sync"
	"time"
)

type Node struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Status      model.NodeState `json:"status"`
	Type        model.NodeType  `json:"type"`
	ContainerID string          `json:"containerId"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type Link struct {
	ID          string    `json:"id"`
	NodeAID     string    `json:"nodeAId"`
	NodeBID     string    `json:"nodeBId"`
	NetworkID   string    `json:"networkId"`
	NetworkName string    `json:"networkName"`
	CreatedAt   time.Time `json:"createdAt"`
}
type Store struct {
	mu        sync.RWMutex
	nodes     map[string]Node
	links     map[string]Link
	linkIndex map[string]string
}

func NewStore() *Store {
	return &Store{
		nodes:     make(map[string]Node),
		links:     make(map[string]Link),
		linkIndex: make(map[string]string),
	}
}

func (s *Store) AddNode(node Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[node.ID] = node
}

func (s *Store) HasNode(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.nodes[id]
	return ok
}

func (s *Store) GetNode(id string) (Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.nodes[id]
	return node, ok
}

func (s *Store) DeleteNode(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[id]; !ok {
		return false
	}
	delete(s.nodes, id)
	return true
}

func (s *Store) UpdateNodeStatus(id string, status model.NodeState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[id]
	if !ok {
		return false
	}
	node.Status = status
	s.nodes[id] = node
	return true
}

func (s *Store) ListNodes() []Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Node, 0, len(s.nodes))
	for _, node := range s.nodes {
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (s *Store) AddLink(link Link) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links[link.ID] = link
	s.linkIndex[linkKey(link.NodeAID, link.NodeBID)] = link.ID
}

func (s *Store) HasLink(nodeAID, nodeBID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.linkIndex[linkKey(nodeAID, nodeBID)]
	return ok
}

func (s *Store) GetLink(id string) (Link, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	link, ok := s.links[id]
	return link, ok
}

func (s *Store) DeleteLink(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.links[id]
	if !ok {
		return false
	}
	delete(s.links, id)
	delete(s.linkIndex, linkKey(link.NodeAID, link.NodeBID))
	return true
}

func (s *Store) ListLinks() []Link {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Link, 0, len(s.links))
	for _, link := range s.links {
		out = append(out, link)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func newID(prefix string) string {
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
