package routes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/dzschnd/dsim/internal/httputil"
	"log/slog"
	"net/http"
	"os"
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
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next.ServeHTTP(w, r)
	})
}

func requestTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := httputil.WithRequestTimeout(r.Context())
		defer cancel()

		tw := &timeoutWriter{
			header: make(http.Header),
			status: http.StatusOK,
		}
		done := make(chan struct{})

		go func() {
			defer close(done)
			next.ServeHTTP(tw, r.WithContext(ctx))
		}()

		select {
		case <-done:
			tw.writeTo(w)
		case <-ctx.Done():
			httputil.WriteJSONError(w, http.StatusRequestTimeout, "request timed out")
		}
	})
}

type timeoutWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *timeoutWriter) Header() http.Header {
	return w.header
}

func (w *timeoutWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return len(b), nil
}

func (w *timeoutWriter) WriteHeader(status int) {
	w.status = status
}

func (w *timeoutWriter) writeTo(dst http.ResponseWriter) {
	for key, values := range w.header {
		for _, value := range values {
			dst.Header().Add(key, value)
		}
	}
	if w.status != 0 {
		dst.WriteHeader(w.status)
	}
	if w.body.Len() > 0 {
		_, _ = dst.Write(w.body.Bytes())
	}
}

type statusRecorder struct {
	w      http.ResponseWriter
	status int
	body   bytes.Buffer
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
