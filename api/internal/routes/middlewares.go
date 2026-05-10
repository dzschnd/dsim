package routes

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func corsHeader(next http.Handler) http.Handler {
	webURL := os.Getenv("WEB_BASE_URL")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", webURL)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS, PATCH")
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
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	w      http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.w
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.w.Write(b)
}
func (r *statusRecorder) Header() http.Header {
	return r.w.Header()
}
func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.w.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.w.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{
			w:      w,
			status: http.StatusOK,
		}

		next.ServeHTTP(rec, r)

		msg := fmt.Sprintf("HTTP Request\n  status: %d\n  path: %s\n  method:%s\n  duration_ms: %d",
			rec.status,
			r.URL.Path,
			r.Method,
			time.Since(start).Milliseconds(),
		)
		response := bytes.TrimSpace(rec.body.Bytes())
		if len(response) > 0 {
			if json.Valid(response) {
				var pretty bytes.Buffer
				if err := json.Indent(&pretty, response, "  ", "  "); err == nil {
					msg += "\n  response:\n  " + pretty.String()
				} else {
					msg += "\n  response:\n  " + string(response)
				}
			} else {
				msg += "\n  response:\n  " + string(response)
			}
		}
		slog.Info(msg)
	})
}
