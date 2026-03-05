package routes

import (
	"net/http"
	"os"

	"github.com/docker/docker/client"
)

type Server struct {
	docker *client.Client
}

func NewServer(docker *client.Client) *Server {
	return &Server{docker: docker}
}

func NewRouter(s *Server) http.Handler {
	r := http.NewServeMux()

	r.HandleFunc("GET /", pingHandler)
	r.HandleFunc("POST /api/v1/nodes", s.createNodeHandler)

	return corsHeader(jsonHeader(r))
}

func corsHeader(next http.Handler) http.Handler {
	webURL := os.Getenv("WEB_BASE_URL")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", webURL)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
