package nodes

import (
	"sort"

	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

type repository struct {
	store *store.Store
}

func newRepository(store *store.Store) *repository {
	return &repository{store: store}
}

func (r *repository) AddNode(node model.Node) {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()
	r.store.Nodes[node.ID] = node
}

func (r *repository) HasNode(id string) bool {
	r.store.Mu.RLock()
	defer r.store.Mu.RUnlock()
	_, ok := r.store.Nodes[id]
	return ok
}

func (r *repository) GetNode(id string) (model.Node, bool) {
	r.store.Mu.RLock()
	defer r.store.Mu.RUnlock()
	node, ok := r.store.Nodes[id]
	return node, ok
}

func (r *repository) DeleteNode(id string) bool {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()
	if _, ok := r.store.Nodes[id]; !ok {
		return false
	}
	delete(r.store.Nodes, id)
	return true
}

func (r *repository) UpdateNodeStatus(id string, status model.NodeState) bool {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()
	node, ok := r.store.Nodes[id]
	if !ok {
		return false
	}
	node.Status = status
	r.store.Nodes[id] = node
	return true
}

func (r *repository) UpdateInterfaceAddress(nodeID, interfaceName, ipAddr string, prefixLen int) bool {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()

	node, ok := r.store.Nodes[nodeID]
	if !ok {
		return false
	}

	for index, iface := range node.Interfaces {
		if iface.Name != interfaceName {
			continue
		}
		node.Interfaces[index].IPAddr = ipAddr
		node.Interfaces[index].PrefixLen = prefixLen
		r.store.Nodes[nodeID] = node
		return true
	}

	return false
}

func (r *repository) UpdateInterfaceRuntime(nodeID, interfaceID, ipAddr string, prefixLen int) bool {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()

	node, ok := r.store.Nodes[nodeID]
	if !ok {
		return false
	}

	for index, iface := range node.Interfaces {
		if iface.ID != interfaceID {
			continue
		}
		node.Interfaces[index].RuntimeIPAddr = ipAddr
		node.Interfaces[index].RuntimePrefixLen = prefixLen
		r.store.Nodes[nodeID] = node
		return true
	}

	return false
}

func (r *repository) ListNodes() []model.Node {
	r.store.Mu.RLock()
	defer r.store.Mu.RUnlock()
	out := make([]model.Node, 0, len(r.store.Nodes))
	for _, node := range r.store.Nodes {
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}
