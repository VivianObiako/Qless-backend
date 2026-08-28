package api

import (
	"net/http"
	"strings"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
)

// The roster is the owner's alone. Every handler here calls requireOwnerActor,
// which is requireActor plus the owner check — there is no queue in the path to
// hang requireOwner off, because an operator belongs to the business rather
// than to any one queue.
func (s *Server) requireOwnerActor(w http.ResponseWriter, r *http.Request) (queue.Actor, bool) {
	actor, ok := s.requireActor(w, r)
	if !ok {
		return queue.Actor{}, false
	}
	if !actor.IsOwner() {
		writeError(w, queue.ErrUnauthorized)
		return queue.Actor{}, false
	}
	return actor, true
}

// operatorID reads and shape-checks the {operatorId} path value.
func operatorID(r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("operatorId"))
	if id == "" || !storage.IsUUID(id) {
		return "", false
	}
	return id, true
}

type operatorsResponse struct {
	Operators []queue.Operator `json:"operators"`
}

func (s *Server) listOperators(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireOwnerActor(w, r)
	if !ok {
		return
	}

	operators, err := s.store.ListOperators(r.Context(), actor.OwnerID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, operatorsResponse{Operators: operators})
}

type createOperatorRequest struct {
	DisplayName string   `json:"displayName"`
	QueueIDs    []string `json:"queueIds"`
}

// operatorResponse carries the access code only on the two requests that mint
// one. It is never retrievable afterwards — only its hash is stored — which is
// why the owner can always issue a replacement.
type operatorResponse struct {
	Operator   queue.Operator `json:"operator"`
	AccessCode string         `json:"accessCode,omitempty"`
}

func validateDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", invalid("Enter a name for this operator.")
	}
	if len([]rune(name)) > 60 {
		return "", invalid("Operator name must be 60 characters or fewer.")
	}
	return name, nil
}

// validateQueueIDs rejects malformed ids here so they never reach the driver.
// Ids that are well-formed but belong to somebody else are not an error: the
// assignment simply matches no row, so an owner cannot use this to discover
// which queue ids exist.
func validateQueueIDs(ids []string) ([]string, error) {
	assigned := make([]string, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if !storage.IsUUID(trimmed) {
			return nil, invalid("One of those queues doesn't look right.")
		}
		assigned = append(assigned, trimmed)
	}
	return assigned, nil
}

func (s *Server) createOperator(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireOwnerActor(w, r)
	if !ok {
		return
	}

	var req createOperatorRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}

	name, err := validateDisplayName(req.DisplayName)
	if err != nil {
		writeError(w, err)
		return
	}

	queueIDs, err := validateQueueIDs(req.QueueIDs)
	if err != nil {
		writeError(w, err)
		return
	}

	accessCode, err := token.NewCode()
	if err != nil {
		writeError(w, err)
		return
	}

	operator, err := s.store.CreateOperator(r.Context(), storage.CreateOperatorParams{
		OwnerID:        actor.OwnerID,
		DisplayName:    name,
		AccessCodeHash: token.HashCode(accessCode),
		QueueIDs:       queueIDs,
	})
	if err != nil {
		writeError(w, err)
		return
	}

	httpx.JSON(w, http.StatusCreated, operatorResponse{Operator: operator, AccessCode: accessCode})
}

type updateOperatorRequest struct {
	DisplayName *string   `json:"displayName"`
	QueueIDs    *[]string `json:"queueIds"`
}

// updateOperator renames and reassigns. Sending queueIds as an empty array is
// how an owner takes every queue away without revoking the person.
func (s *Server) updateOperator(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireOwnerActor(w, r)
	if !ok {
		return
	}

	id, ok := operatorID(r)
	if !ok {
		writeError(w, queue.ErrOperatorNotFound)
		return
	}

	var req updateOperatorRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}

	params := storage.UpdateOperatorParams{}

	if req.DisplayName != nil {
		name, err := validateDisplayName(*req.DisplayName)
		if err != nil {
			writeError(w, err)
			return
		}
		params.DisplayName = &name
	}

	if req.QueueIDs != nil {
		queueIDs, err := validateQueueIDs(*req.QueueIDs)
		if err != nil {
			writeError(w, err)
			return
		}
		params.QueueIDs = &queueIDs
	}

	operator, err := s.store.UpdateOperator(r.Context(), actor.OwnerID, id, params)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, operatorResponse{Operator: operator})
}

// regenerateOperatorCode issues a replacement and retires the old one at once.
// An operator's code is handed over in person by somebody who can hand over
// another, so there is nothing here like the owner's two-step rotation.
func (s *Server) regenerateOperatorCode(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireOwnerActor(w, r)
	if !ok {
		return
	}

	id, ok := operatorID(r)
	if !ok {
		writeError(w, queue.ErrOperatorNotFound)
		return
	}

	accessCode, err := token.NewCode()
	if err != nil {
		writeError(w, err)
		return
	}

	if err := s.store.RegenerateOperatorCode(r.Context(), actor.OwnerID, id, token.HashCode(accessCode)); err != nil {
		writeError(w, err)
		return
	}

	operator, err := s.store.GetOperator(r.Context(), actor.OwnerID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, operatorResponse{Operator: operator, AccessCode: accessCode})
}

// revokeOperator ends their access everywhere, immediately. The person is kept
// on the roster as revoked so the entries they handled keep resolving to a
// name; deleting them would quietly rewrite history.
func (s *Server) revokeOperator(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireOwnerActor(w, r)
	if !ok {
		return
	}

	id, ok := operatorID(r)
	if !ok {
		writeError(w, queue.ErrOperatorNotFound)
		return
	}

	if err := s.store.RevokeOperator(r.Context(), actor.OwnerID, id); err != nil {
		writeError(w, err)
		return
	}

	operator, err := s.store.GetOperator(r.Context(), actor.OwnerID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, operatorResponse{Operator: operator})
}
