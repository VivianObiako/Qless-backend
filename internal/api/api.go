// Package api exposes the Qless HTTP surface. Handlers stay thin: they
// validate input, call storage, and shape a response. Queue rules live in the
// storage transactions and the database constraints.
package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/realtime"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
)

type Server struct {
	store *storage.Store

	hub     *realtime.Hub
	sockets *realtime.Upgrader

	joinLimiter   *httpx.Limiter
	writeLimiter  *httpx.Limiter
	socketLimiter *httpx.Limiter

	redeemLimiter *httpx.Limiter
	codeLimiter   *httpx.Limiter
	redeemLockout *httpx.Lockout
}

func NewServer(store *storage.Store) *Server {
	return &Server{
		store: store,
		hub:   realtime.NewHub(),
		// 5 joins per minute per IP per queue, and a wider ceiling on writes
		// overall. Enough to stop a bored person filling a barbershop queue,
		// not so tight that a family joining on one phone hits it.
		joinLimiter:  httpx.NewLimiter(5, 5),
		writeLimiter: httpx.NewLimiter(30, 30),
		// Connections, not messages: a browser reconnecting with backoff after
		// a flaky signal should never be locked out, but a script opening
		// sockets in a loop should be.
		socketLimiter: httpx.NewLimiter(30, 10),

		// Redeem is the only endpoint where guessing wins something, so it is
		// paced far harder than the rest. Ten attempts a minute from one address
		// is generous for someone copying a code off paper and derisory for
		// anyone working through a keyspace.
		redeemLimiter: httpx.NewLimiter(10, 5),
		// The second key is the code's own first group, which caps how fast a
		// botnet spread across many addresses can sweep codes that begin alike.
		codeLimiter: httpx.NewLimiter(10, 5),
		// And a limiter alone only paces an attacker, because it refills. Twenty
		// wrong codes from one address inside ten minutes closes it for fifteen.
		redeemLockout: httpx.NewLockout(20, 10*time.Minute, 15*time.Minute),
	}
}

// resolveQueue turns the {key} path value into a queue, answering 404 for
// unknown slugs and malformed ids alike.
func (s *Server) resolveQueue(w http.ResponseWriter, r *http.Request) (queue.Queue, bool) {
	key := r.PathValue("key")
	if key == "" {
		writeError(w, queue.ErrNotFound)
		return queue.Queue{}, false
	}

	q, err := s.store.GetQueue(r.Context(), key)
	if err != nil {
		writeError(w, err)
		return queue.Queue{}, false
	}
	return q, true
}

// requireActor resolves the bearer token to whoever is holding it, without
// reference to any particular queue. A missing token and an unknown one get the
// same answer: the response never confirms that a token was once real.
func (s *Server) requireActor(w http.ResponseWriter, r *http.Request) (queue.Actor, bool) {
	raw := bearerToken(r)
	if raw == "" {
		writeError(w, queue.ErrUnauthorized)
		return queue.Actor{}, false
	}

	actor, err := s.store.ResolveActor(r.Context(), token.Hash(raw))
	if err != nil {
		writeError(w, err)
		return queue.Actor{}, false
	}
	return actor, true
}

// requireQueueAccess admits anyone with a path to this queue: the owner today,
// an assigned operator from 00003. It backs the actions the permission table
// shares between the two — serve, skip, call, pause, resume — so that adding
// operators changes what AuthorizeQueue answers, not what these handlers ask.
//
// Every administrative handler calls one of these itself; there is deliberately
// no middleware, so a new route cannot forget to be covered by one.
func (s *Server) requireQueueAccess(w http.ResponseWriter, r *http.Request) (queue.Queue, queue.Actor, bool) {
	q, ok := s.resolveQueue(w, r)
	if !ok {
		return queue.Queue{}, queue.Actor{}, false
	}

	actor, ok := s.requireActor(w, r)
	if !ok {
		return queue.Queue{}, queue.Actor{}, false
	}

	if err := s.store.AuthorizeQueue(r.Context(), actor, q.ID); err != nil {
		writeError(w, err)
		return queue.Queue{}, queue.Actor{}, false
	}
	return q, actor, true
}

// requireOwner narrows that to the owner alone: closing, resetting, settings,
// and managing operators are the business's own decisions, not the counter's.
func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request) (queue.Queue, queue.Actor, bool) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return queue.Queue{}, queue.Actor{}, false
	}

	if !actor.IsOwner() {
		writeError(w, queue.ErrUnauthorized)
		return queue.Queue{}, queue.Actor{}, false
	}
	return q, actor, true
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	value, found := strings.CutPrefix(header, "Bearer ")
	if !found {
		return ""
	}
	return strings.TrimSpace(value)
}

// writeError maps a domain error onto a status code and a message written for
// whoever is holding the phone. Unexpected errors are logged and flattened so
// no driver detail reaches the browser.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, queue.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "queue_not_found", "We couldn't find this queue.")
	case errors.Is(err, queue.ErrEntryNotFound):
		httpx.WriteError(w, http.StatusNotFound, "entry_not_found", "We couldn't find that customer.")
	case errors.Is(err, queue.ErrOperatorNotFound):
		httpx.WriteError(w, http.StatusNotFound, "operator_not_found", "We couldn't find that operator.")
	case errors.Is(err, queue.ErrEntryNotActive):
		httpx.WriteError(w, http.StatusConflict, "entry_not_active", "That customer has already been dealt with.")
	case errors.Is(err, queue.ErrNotInQueue):
		httpx.WriteError(w, http.StatusNotFound, "not_in_queue", "Your previous queue position is no longer active.")
	case errors.Is(err, queue.ErrQueuePaused):
		httpx.WriteError(w, http.StatusConflict, "queue_paused", "This queue is temporarily paused.")
	case errors.Is(err, queue.ErrQueueClosed):
		httpx.WriteError(w, http.StatusConflict, "queue_closed", "This queue is closed.")
	case errors.Is(err, queue.ErrQueueFull):
		httpx.WriteError(w, http.StatusConflict, "queue_full", "This queue is currently full.")
	case errors.Is(err, queue.ErrUnauthorized):
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "You don't have permission to manage this queue.")
	case errors.Is(err, queue.ErrInvalidCode):
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_code", "That code isn't valid. Check it and try again.")
	case errors.Is(err, queue.ErrInvalidInput):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_input", strings.TrimPrefix(err.Error(), "invalid input: "))
	default:
		slog.Error("unhandled error", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong on our end.")
	}
}

// invalid wraps ErrInvalidInput with a message intended for display, so
// validation failures reach the user as guidance rather than as a code.
func invalid(message string) error {
	return fmt.Errorf("%w: %s", queue.ErrInvalidInput, message)
}

