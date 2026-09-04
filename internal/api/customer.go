package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/token"
)

type joinRequest struct {
	Name string `json:"name"`
}

// joinResponse carries the customer token only when one was newly issued. The
// browser stores it and presents it on every later request.
type joinResponse struct {
	CustomerView
	CustomerToken string `json:"customerToken"`
	AlreadyJoined bool   `json:"alreadyJoined"`
}

func (s *Server) joinQueue(w http.ResponseWriter, r *http.Request) {
	q, ok := s.resolveQueue(w, r)
	if !ok {
		return
	}

	if !s.joinLimiter.Allow(httpx.ClientIP(r) + "|" + q.ID) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "Too many join attempts. Try again in a minute.")
		return
	}

	var req joinRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, invalid("Enter your name to join the queue."))
		return
	}
	if len([]rune(name)) > 60 {
		writeError(w, invalid("Name must be 60 characters or fewer."))
		return
	}

	// Reuse the token the browser already holds so a customer who rejoins
	// after leaving keeps a single identity for this queue.
	customerToken := strings.TrimSpace(r.Header.Get(httpx.CustomerTokenHeader))
	if customerToken == "" {
		issued, err := token.New()
		if err != nil {
			writeError(w, err)
			return
		}
		customerToken = issued
	}

	entry, err := s.store.Join(r.Context(), q.ID, name, token.Hash(customerToken))
	alreadyJoined := errors.Is(err, queue.ErrAlreadyJoined)
	if err != nil && !alreadyJoined {
		writeError(w, err)
		return
	}

	// A repeat join returns the existing entry and changes nothing, so it is
	// not something the queue needs to hear about.
	if !alreadyJoined {
		s.publish(r.Context(), q.ID, EventCustomerJoined)
	}

	view, err := s.customerView(r.Context(), q, &entry)
	if err != nil {
		writeError(w, err)
		return
	}

	status := http.StatusCreated
	if alreadyJoined {
		status = http.StatusOK
	}

	httpx.JSON(w, status, joinResponse{
		CustomerView:  view,
		CustomerToken: customerToken,
		AlreadyJoined: alreadyJoined,
	})
}

// getMe recovers a customer's entry from their browser token. This is what
// makes rescanning the QR code after closing the browser work.
func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	q, ok := s.resolveQueue(w, r)
	if !ok {
		return
	}

	raw := strings.TrimSpace(r.Header.Get(httpx.CustomerTokenHeader))
	if raw == "" {
		writeError(w, queue.ErrNotInQueue)
		return
	}

	entry, err := s.store.MyEntry(r.Context(), q.ID, token.Hash(raw))
	if err != nil {
		writeError(w, err)
		return
	}

	view, err := s.customerView(r.Context(), q, &entry)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

func (s *Server) leaveQueue(w http.ResponseWriter, r *http.Request) {
	q, ok := s.resolveQueue(w, r)
	if !ok {
		return
	}

	raw := strings.TrimSpace(r.Header.Get(httpx.CustomerTokenHeader))
	if raw == "" {
		writeError(w, queue.ErrNotInQueue)
		return
	}

	entry, err := s.store.Leave(r.Context(), q.ID, token.Hash(raw))
	if err != nil {
		writeError(w, err)
		return
	}

	s.publish(r.Context(), q.ID, EventCustomerLeft)

	view, err := s.customerView(r.Context(), q, &entry)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

type presenceRequest struct {
	Presence string `json:"presence"`
}

// setPresence records what the customer has told the counter about where they
// are: on the way, here, or needing a moment after being called. It goes on
// their active entry and out to the dashboards on the next frame — and never
// into the public state, which carries numbers only.
func (s *Server) setPresence(w http.ResponseWriter, r *http.Request) {
	q, ok := s.resolveQueue(w, r)
	if !ok {
		return
	}

	raw := strings.TrimSpace(r.Header.Get(httpx.CustomerTokenHeader))
	if raw == "" {
		writeError(w, queue.ErrNotInQueue)
		return
	}

	var req presenceRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}

	presence := queue.Presence(strings.ToUpper(strings.TrimSpace(req.Presence)))
	if !presence.Valid() {
		writeError(w, invalid("Say whether you're on the way, here, or need a moment."))
		return
	}

	entry, err := s.store.SetPresence(r.Context(), q.ID, token.Hash(raw), presence)
	if err != nil {
		writeError(w, err)
		return
	}

	s.publish(r.Context(), q.ID, EventCustomerPresence)

	view, err := s.customerView(r.Context(), q, &entry)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}
