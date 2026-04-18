package routes

import (
	"net/http"

	"github.com/docker/docker/client"
	"github.com/dzschnd/dsim/internal/links"
	"github.com/dzschnd/dsim/internal/nodes"
	"github.com/dzschnd/dsim/internal/store"
)

type Server struct {
	docker *client.Client
	store  *store.Store
}

func NewServer(docker *client.Client, store *store.Store) *Server {
	return &Server{docker: docker, store: store}
}

func NewRouter(s *Server) http.Handler {
	r := http.NewServeMux()
	n := nodes.NewHandler(s.docker, s.store)
	l := links.NewHandler(s.docker, s.store)

	r.HandleFunc("POST /api/v1/nodes", n.CreateNodeHandler)
	r.HandleFunc("GET /api/v1/nodes", n.ListNodesHandler)
	r.HandleFunc("DELETE /api/v1/nodes/{id}", n.DeleteNodeHandler)
	r.HandleFunc("POST /api/v1/nodes/{id}/start", n.StartNodeHandler)
	r.HandleFunc("POST /api/v1/nodes/{id}/stop", n.StopNodeHandler)
	r.HandleFunc("POST /api/v1/nodes/{id}/cli", n.CLIHandler)

	r.HandleFunc("POST /api/v1/links", l.CreateLinkHandler)
	r.HandleFunc("GET /api/v1/links", l.ListLinksHandler)
	r.HandleFunc("DELETE /api/v1/links/{id}", l.DeleteLinkHandler)

	return requestLogger(corsHeader(jsonHeader(r)))
}
