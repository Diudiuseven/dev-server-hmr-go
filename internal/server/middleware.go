package server

import (
	"log"
	"net/http"
	"time"
)

// logging adds minimal request logging and recovers from handler panics so
// one bad request can never take the server down. It transparently forwards
// optional interfaces (Flusher, Hijacker) implemented by the wrapped writer.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic serving %s: %v", r.URL.Path, rec)
			}
			log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, sw.status, time.Since(start))
		}()
		next.ServeHTTP(sw, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
		s.ResponseWriter.WriteHeader(code)
	}
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush forwards to the underlying Flusher so SSE streaming works.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
