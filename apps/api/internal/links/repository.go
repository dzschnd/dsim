package links

import (
	"github.com/dzschnd/dsim/internal/model"
	"github.com/dzschnd/dsim/internal/store"
)

type repository struct {
	store *store.Store
}

func newRepository(store *store.Store) *repository {
	return &repository{store: store}
}

func (r *repository) AddLink(link model.Link) {
	r.store.AddLink(link)
}

func (r *repository) HasLink(nodeAID, nodeBID string) bool {
	return r.store.HasLink(nodeAID, nodeBID)
}

func (r *repository) GetLink(id string) (model.Link, bool) {
	return r.store.GetLink(id)
}

func (r *repository) DeleteLink(id string) bool {
	return r.store.DeleteLink(id)
}

func (r *repository) ListLinks() []model.Link {
	return r.store.ListLinks()
}

func (r *repository) GetNode(id string) (model.Node, bool) {
	r.store.Mu.RLock()
	defer r.store.Mu.RUnlock()
	node, ok := r.store.Nodes[id]
	return node, ok
}
