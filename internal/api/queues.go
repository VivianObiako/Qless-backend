package api

import (
	"net/http"
	"strings"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
)

type createQueueRequest struct {
	Name                  string `json:"name"`
	Description           string `json:"description"`
	AverageServiceMinutes int    `json:"averageServiceMinutes"`
	MaxCapacity           *int   `json:"maxCapacity"`
}

func (r createQueueRequest) validate() (storage.CreateQueueParams, error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return storage.CreateQueueParams{}, invalid("Enter a name for your queue.")
	}
	if len([]rune(name)) > 80 {
		return storage.CreateQueueParams{}, invalid("Queue name must be 80 characters or fewer.")
	}

	description := strings.TrimSpace(r.Description)
	if len([]rune(description)) > 200 {
		return storage.CreateQueueParams{}, invalid("Description must be 200 characters or fewer.")
	}

	if r.AverageServiceMinutes < 1 || r.AverageServiceMinutes > 480 {
		return storage.CreateQueueParams{}, invalid("Average service time must be between 1 and 480 minutes.")
	}

	if r.MaxCapacity != nil && (*r.MaxCapacity < 1 || *r.MaxCapacity > 1000) {
		return storage.CreateQueueParams{}, invalid("Maximum queue size must be between 1 and 1000.")
	}

	return storage.CreateQueueParams{
		Name:                  name,
		Description:           description,
		AverageServiceMinutes: r.AverageServiceMinutes,
		MaxCapacity:           r.MaxCapacity,
	}, nil
}

// createQueueResponse hands back a session token, and — for a business that did
// not exist a moment ago — the recovery code, exactly once. Neither is
// retrievable afterwards; only their hashes are stored.
type createQueueResponse struct {
	Queue      queue.Queue `json:"queue"`
	OwnerToken string      `json:"ownerToken"`

	// RecoveryCode is present only when this request created the business. An
	// owner adding a second queue already has one, and it cannot be shown again.
	RecoveryCode string `json:"recoveryCode,omitempty"`
}

// createQueue makes a queue, and makes a business if the caller does not
// already have one.
//
// An owner presenting a session token gets the queue added to the business they
// already hold, which is what makes "my queues" a real list rather than a pile
// of unrelated links.
func (s *Server) createQueue(w http.ResponseWriter, r *http.Request) {
	if !s.writeLimiter.Allow(httpx.ClientIP(r)) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Try again in a moment.")
		return
	}

	var req createQueueRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}

	params, err := req.validate()
	if err != nil {
		writeError(w, err)
		return
	}

	sessionToken := bearerToken(r)
	var recoveryCode string

	if sessionToken != "" {
		// A token that is present but unknown is refused rather than treated as
		// a new business. Quietly minting a second owner would scatter one
		// person's queues across two of them, invisibly and permanently.
		actor, err := s.store.ResolveActor(r.Context(), token.Hash(sessionToken))
		if err != nil {
			writeError(w, err)
			return
		}
		if !actor.IsOwner() {
			writeError(w, queue.ErrUnauthorized)
			return
		}
		params.OwnerID = actor.OwnerID
	} else {
		sessionToken, err = token.New()
		if err != nil {
			writeError(w, err)
			return
		}
		recoveryCode, err = token.NewCode()
		if err != nil {
			writeError(w, err)
			return
		}
		params.NewOwnerTokenHash = token.Hash(sessionToken)
		params.NewOwnerRecoveryCodeHash = token.HashCode(recoveryCode)
	}

	created, err := s.store.CreateQueue(r.Context(), params)
	if err != nil {
		writeError(w, err)
		return
	}

	// An existing owner is handed back the token they arrived with. It grants
	// them nothing they did not already have, and it lets one client path cover
	// both cases until the web app moves onto sessions in phase 2.
	httpx.JSON(w, http.StatusCreated, createQueueResponse{
		Queue:        created.Queue,
		OwnerToken:   sessionToken,
		RecoveryCode: recoveryCode,
	})
}

// getQueue returns the public state, enriched with the caller's own entry when
// they present a customer token. One request is enough to render the customer
// page in either the "not joined" or "waiting" state.
func (s *Server) getQueue(w http.ResponseWriter, r *http.Request) {
	q, ok := s.resolveQueue(w, r)
	if !ok {
		return
	}

	entry := s.lookupEntry(r, q)

	view, err := s.customerView(r.Context(), q, entry)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

// lookupEntry resolves the caller's entry from their customer token, treating
// absence as "not joined" rather than as an error.
func (s *Server) lookupEntry(r *http.Request, q queue.Queue) *queue.Entry {
	raw := strings.TrimSpace(r.Header.Get(httpx.CustomerTokenHeader))
	if raw == "" {
		return nil
	}

	entry, err := s.store.MyEntry(r.Context(), q.ID, token.Hash(raw))
	if err != nil {
		return nil
	}
	return &entry
}

