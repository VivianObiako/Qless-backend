package api

import (
	"net/http"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/realtime"
)

type healthResponse struct {
	Status string `json:"status"`
}

// Routes returns the fully wired handler. Operator routes are distinguished
// only by the handler they point at — each one calls requireOwner or
// requireQueueAccess itself, so adding a route cannot accidentally skip the
// check by missing a middleware.
func (s *Server) Routes(allowedOrigins ...string) http.Handler {
	// The upgrader is built here because only this call knows the web origins,
	// and a WebSocket handshake is not covered by the CORS middleware below.
	s.sockets = realtime.NewUpgrader(s.hub, allowedOrigins...)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, healthResponse{Status: "ok"})
	})

	// Identity. A code is redeemed once for a session token; the token is what
	// every other route below reads. Neither says which queue — the server
	// answers that from who the caller turns out to be.
	mux.HandleFunc("POST /api/access/redeem", s.redeemCode)
	mux.HandleFunc("POST /api/access/recovery-code/acknowledge", s.acknowledgeRecoveryCode)
	mux.HandleFunc("GET /api/me/queues", s.myQueues)
	mux.HandleFunc("POST /api/sessions/revoke-others", s.revokeOtherSessions)

	// The roster. Owner only, and scoped to the business rather than to any one
	// queue — an operator can cover several.
	mux.HandleFunc("GET /api/operators", s.listOperators)
	mux.HandleFunc("POST /api/operators", s.createOperator)
	mux.HandleFunc("PATCH /api/operators/{operatorId}", s.updateOperator)
	mux.HandleFunc("POST /api/operators/{operatorId}/code", s.regenerateOperatorCode)
	mux.HandleFunc("POST /api/operators/{operatorId}/revoke", s.revokeOperator)

	// Public and customer routes. {key} accepts either a queue id or a slug.
	mux.HandleFunc("POST /api/queues", s.createQueue)
	mux.HandleFunc("GET /api/queues/{key}", s.getQueue)
	mux.HandleFunc("POST /api/queues/{key}/join", s.joinQueue)
	mux.HandleFunc("GET /api/queues/{key}/me", s.getMe)
	mux.HandleFunc("POST /api/queues/{key}/leave", s.leaveQueue)

	// Realtime. Public by default; operator frames require ?k= on the
	// handshake, verified in the handler.
	mux.HandleFunc("GET /api/queues/{key}/ws", s.queueSocket)

	// Operator routes. Which check each one runs is the permission table:
	// working the counter is shared with operators, changing the business is
	// not. See requireQueueAccess and requireOwner.
	mux.HandleFunc("GET /api/queues/{key}/entries", s.listEntries)
	mux.HandleFunc("GET /api/queues/{key}/history", s.queueHistory)
	mux.HandleFunc("PATCH /api/queues/{key}", s.updateQueue)

	mux.HandleFunc("POST /api/queues/{key}/next", s.serveNext)
	mux.HandleFunc("POST /api/queues/{key}/entries/{entryId}/serve", s.serveEntry)
	mux.HandleFunc("POST /api/queues/{key}/entries/{entryId}/attend", s.attendEntry)
	mux.HandleFunc("POST /api/queues/{key}/entries/{entryId}/skip", s.skipEntry)

	mux.HandleFunc("POST /api/queues/{key}/pause", s.pauseQueue)
	mux.HandleFunc("POST /api/queues/{key}/resume", s.resumeQueue)
	mux.HandleFunc("POST /api/queues/{key}/close", s.closeQueue)
	mux.HandleFunc("POST /api/queues/{key}/reset", s.resetQueue)

	return httpx.Chain(mux,
		httpx.Recoverer,
		httpx.Logger,
		httpx.CORS(allowedOrigins...),
	)
}
