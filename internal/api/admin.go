package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/storage"
)

// WithAdmin turns on the operator's stats route. An empty token leaves it off,
// and the route answers 404 as if it did not exist.
func (s *Server) WithAdmin(adminToken string) *Server {
	s.adminToken = adminToken
	return s
}

type adminHealth struct {
	DatabaseMs int64  `json:"databaseMs"`
	UptimeSec  int64  `json:"uptimeSec"`
	Commit     string `json:"commit"`
	Push       bool   `json:"push"`
}

type adminResponse struct {
	Health adminHealth        `json:"health"`
	Stats  storage.AdminStats `json:"stats"`
}

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" {
		http.NotFound(w, r)
		return
	}
	if !s.adminLimiter.Allow(httpx.ClientIP(r)) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Try again in a moment.")
		return
	}
	if subtle.ConstantTimeCompare([]byte(bearerToken(r)), []byte(s.adminToken)) != 1 {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "That admin key is not right.")
		return
	}

	start := time.Now()
	if err := s.store.Ping(r.Context()); err != nil {
		slog.Error("admin: ping database", "error", err)
		httpx.WriteError(w, http.StatusServiceUnavailable, "database_down", "The database did not answer.")
		return
	}
	dbTime := time.Since(start)

	stats, err := s.store.AdminStats(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}

	commit := os.Getenv("RENDER_GIT_COMMIT")
	if len(commit) > 7 {
		commit = commit[:7]
	}
	httpx.JSON(w, http.StatusOK, adminResponse{
		Health: adminHealth{
			DatabaseMs: dbTime.Milliseconds(),
			UptimeSec:  int64(time.Since(s.startedAt).Seconds()),
			Commit:     commit,
			Push:       s.push.Enabled(),
		},
		Stats: stats,
	})
}
