package links

import (
	"sort"

	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

type Repository struct {
	store *store.Store
}

func NewRepository(store *store.Store) *Repository {
	return &Repository{store: store}
}

func linkKey(a, b string) string {
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}

func (r *Repository) AddLink(link model.Link) {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()
	r.store.Links[link.ID] = link
	r.store.LinkIndex[linkKey(link.InterfaceAID, link.InterfaceBID)] = link.ID
}

func (r *Repository) HasLink(interfaceAID, interfaceBID string) bool {
	r.store.Mu.RLock()
	defer r.store.Mu.RUnlock()
	_, ok := r.store.LinkIndex[linkKey(interfaceAID, interfaceBID)]
	return ok
}

func (r *Repository) GetLink(id string) (model.Link, bool) {
	r.store.Mu.RLock()
	defer r.store.Mu.RUnlock()
	link, ok := r.store.Links[id]
	return link, ok
}

func (r *Repository) DeleteLink(id string) bool {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()
	link, ok := r.store.Links[id]
	if !ok {
		return false
	}
	delete(r.store.Links, id)
	delete(r.store.LinkIndex, linkKey(link.InterfaceAID, link.InterfaceBID))
	return true
}

func (r *Repository) DeleteLinkByNode(nodeID string) {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()

	for linkID, link := range r.store.Links {
		if !r.nodeOwnsInterfaceLocked(nodeID, link.InterfaceAID) && !r.nodeOwnsInterfaceLocked(nodeID, link.InterfaceBID) {
			continue
		}
		r.setInterfaceLinkLocked(link.InterfaceAID, "")
		r.setInterfaceLinkLocked(link.InterfaceBID, "")
		delete(r.store.Links, linkID)
		delete(r.store.LinkIndex, linkKey(link.InterfaceAID, link.InterfaceBID))
	}
}

func (r *Repository) ListLinks() []model.Link {
	r.store.Mu.RLock()
	defer r.store.Mu.RUnlock()
	out := make([]model.Link, 0, len(r.store.Links))
	for _, link := range r.store.Links {
		out = append(out, link)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (r *Repository) GetNode(id string) (model.Node, bool) {
	r.store.Mu.RLock()
	defer r.store.Mu.RUnlock()
	node, ok := r.store.Nodes[id]
	return node, ok
}

func (r *Repository) GetNodeByInterface(interfaceID string) (model.Node, model.Interface, bool) {
	r.store.Mu.RLock()
	defer r.store.Mu.RUnlock()

	nodeID, ok := r.store.InterfaceOwnerIndex[interfaceID]
	if !ok {
		return model.Node{}, model.Interface{}, false
	}

	node, ok := r.store.Nodes[nodeID]
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

func (r *Repository) SetInterfaceLink(interfaceID, linkID string) bool {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()
	return r.setInterfaceLinkLocked(interfaceID, linkID)
}

func (r *Repository) SetInterfaceRuntime(interfaceID, ipAddr string, prefixLen int) bool {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()

	nodeID, ok := r.store.InterfaceOwnerIndex[interfaceID]
	if !ok {
		return false
	}

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

func (r *Repository) SetInterfaceRuntimeName(interfaceID, runtimeName string) bool {
	r.store.Mu.Lock()
	defer r.store.Mu.Unlock()

	nodeID, ok := r.store.InterfaceOwnerIndex[interfaceID]
	if !ok {
		return false
	}

	node, ok := r.store.Nodes[nodeID]
	if !ok {
		return false
	}

	for index, iface := range node.Interfaces {
		if iface.ID != interfaceID {
			continue
		}
		node.Interfaces[index].RuntimeName = runtimeName
		r.store.Nodes[nodeID] = node
		return true
	}

	return false
}

func (r *Repository) setInterfaceLinkLocked(interfaceID, linkID string) bool {
	nodeID, ok := r.store.InterfaceOwnerIndex[interfaceID]
	if !ok {
		return false
	}

	node, ok := r.store.Nodes[nodeID]
	if !ok {
		return false
	}

	for index, iface := range node.Interfaces {
		if iface.ID != interfaceID {
			continue
		}
		node.Interfaces[index].LinkID = linkID
		r.store.Nodes[nodeID] = node
		return true
	}

	return false
}

func (r *Repository) nodeOwnsInterfaceLocked(nodeID, interfaceID string) bool {
	ownerID, ok := r.store.InterfaceOwnerIndex[interfaceID]
	return ok && ownerID == nodeID
}
