package httpx

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first argument is the outermost layer.
func Chain(h http.Handler, middleware ...Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

// Recoverer keeps one panicking handler from taking the queue offline.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("panic in handler", "error", recovered, "path", r.URL.Path)
				WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong on our end.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

// Hijack hands the raw connection to the WebSocket upgrader. Wrapping a
// ResponseWriter in a struct hides every interface the original also
// implemented, so without this every upgrade fails with a 500.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(start).Round(time.Millisecond).String(),
		)
	})
}

// OriginAllowed reports whether origin is one this API answers to. A "*"
// anywhere in the list opens it to every origin, which is what the tests use.
//
// The WebSocket handshake asks the same question through this function rather
// than repeating the rule, because a handshake is not covered by CORS and the
// two checks drifting apart is how a queue ends up readable by another site.
func OriginAllowed(allowed []string, origin string) bool {
	for _, candidate := range allowed {
		if candidate == "*" || candidate == origin {
			return true
		}
	}
	return false
}

// CORS allows the Next.js origin to call the API during development and in a
// split deployment where web and API sit on different hosts.
func CORS(allowedOrigins ...string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && OriginAllowed(allowedOrigins, origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+CustomerTokenHeader)
				w.Header().Set("Access-Control-Max-Age", "600")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ConnectingIPHeader is set by Cloudflare, which sits in front of every Render
// service, to the address that connected to it. Cloudflare overwrites any
// value a client sends, so unlike X-Forwarded-For it cannot be forged.
const ConnectingIPHeader = "CF-Connecting-IP"

// ClientIP is the address rate limits are keyed on.
//
// Render appends to X-Forwarded-For rather than replacing it, so its first
// entry is whatever the caller wrote there: one made-up header was enough to
// step around every limit. Cloudflare's own header comes first for that
// reason. X-Forwarded-For stays as the fallback for a deploy without
// Cloudflare in front, where it is no worse than it was, and the socket
// address is the last resort for a request that came straight in.
func ClientIP(r *http.Request) string {
	if connecting := strings.TrimSpace(r.Header.Get(ConnectingIPHeader)); connecting != "" {
		return connecting
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, found := strings.Cut(forwarded, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
