package routes

import (
	"net/http"
	"os"

	"github.com/docker/docker/client"
)

type Server struct {
	docker *client.Client
	store  *Store
}

func NewServer(docker *client.Client, store *Store) *Server {
	return &Server{docker: docker, store: store}
}

func NewRouter(s *Server) http.Handler {
	r := http.NewServeMux()

	r.HandleFunc("GET /", pingHandler)
	r.HandleFunc("POST /api/v1/nodes", s.createNodeHandler)
	r.HandleFunc("GET /api/v1/nodes", s.listNodesHandler)
	r.HandleFunc("DELETE /api/v1/nodes/{id}", s.deleteNodeHandler)
	r.HandleFunc("POST /api/v1/links", s.createLinkHandler)
	r.HandleFunc("GET /api/v1/links", s.listLinksHandler)
	r.HandleFunc("DELETE /api/v1/links/{id}", s.deleteLinkHandler)

	return corsHeader(jsonHeader(r))
}

func corsHeader(next http.Handler) http.Handler {
	webURL := os.Getenv("WEB_BASE_URL")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", webURL)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next.ServeHTTP(w, r)
	})
}
