package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/token"
)

// redeemRequest carries whatever the person typed. Spacing, case and the
// dashes are normalised away before it is hashed, so a code read off a printed
// sheet matches a code pasted out of a message.
type redeemRequest struct {
	Code string `json:"code"`
}

// redeemResponse is the same shape for both kinds of code. The role is the
// server's answer, never the client's claim: the code that matched decides it.
type redeemResponse struct {
	Role   queue.PrincipalType `json:"role"`
	Token  string              `json:"token"`
	Queues []queue.QueueCard   `json:"queues"`

	// RecoveryCode is the owner's replacement code, returned once. It is not
	// live until the client acknowledges it — see acknowledgeRecoveryCode.
	RecoveryCode string `json:"recoveryCode,omitempty"`
}

// redeemCode exchanges a typed code for a session token.
//
// This is the one endpoint where guessing wins something, so it is written to
// give nothing away. Every failure — a code that never existed, one that has
// been rotated away, an empty string — takes the same path, does the same work
// and returns the same body, so neither the response nor the time it took says
// whether a code exists.
func (s *Server) redeemCode(w http.ResponseWriter, r *http.Request) {
	ip := httpx.ClientIP(r)

	if s.redeemLockout.Locked(ip) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later.")
		return
	}
	if !s.redeemLimiter.Allow(ip) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again in a moment.")
		return
	}

	var req redeemRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}

	// Deliberately not validated for shape. Rejecting a malformed code early
	// would answer faster than rejecting a well-formed one, which is a
	// difference worth measuring; normalising anything at all into a lookup
	// leaves one path for every wrong answer.
	code := strings.TrimSpace(req.Code)
	if !s.codeLimiter.Allow("code:" + token.CodePrefix(code)) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again in a moment.")
		return
	}

	// Owner codes first, then operator codes. Whichever table the hash is found
	// in is what settles the role — the client never says which kind it holds,
	// and both kinds are typed into the same box.
	hash := token.HashCode(code)

	ownerID, err := s.store.OwnerByRecoveryCode(r.Context(), hash)
	switch {
	case err == nil:
		s.redeemAsOwner(w, r, ip, ownerID, hash)
		return
	case !errors.Is(err, queue.ErrInvalidCode):
		// A database that is down is not a wrong answer. Counting it as one
		// would lock people out of their own business over an outage.
		writeError(w, err)
		return
	}

	operatorID, err := s.store.OperatorByAccessCode(r.Context(), hash)
	switch {
	case err == nil:
		s.redeemAsOperator(w, r, ip, operatorID)
		return
	case !errors.Is(err, queue.ErrInvalidCode):
		writeError(w, err)
		return
	}

	s.redeemLockout.Fail(ip)
	writeError(w, queue.ErrInvalidCode)
}

func (s *Server) redeemAsOwner(
	w http.ResponseWriter,
	r *http.Request,
	ip, ownerID, redeemedHash string,
) {
	sessionToken, err := token.New()
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.IssueOwnerToken(r.Context(), ownerID, token.Hash(sessionToken)); err != nil {
		writeError(w, err)
		return
	}

	// Recovering does not disturb the devices already signed in — the counter
	// tablet keeps its own row. Signing those out is a separate, deliberate act
	// on /api/sessions/revoke-others.
	replacement, err := token.NewCode()
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.RotateRecoveryCode(r.Context(), ownerID, redeemedHash, token.HashCode(replacement)); err != nil {
		writeError(w, err)
		return
	}

	actor := queue.Actor{Type: queue.PrincipalOwner, ID: ownerID, OwnerID: ownerID}
	queues, err := s.store.QueuesForActor(r.Context(), actor)
	if err != nil {
		writeError(w, err)
		return
	}

	s.redeemLockout.Reset(ip)

	httpx.JSON(w, http.StatusOK, redeemResponse{
		Role:         actor.Type,
		Token:        sessionToken,
		Queues:       queues,
		RecoveryCode: replacement,
	})
}

// redeemAsOperator issues a session and nothing else. An operator's code is
// reusable and stays exactly as it was: it is the same code on the counter
// tablet and on their phone, and only the owner can change or withdraw it.
func (s *Server) redeemAsOperator(w http.ResponseWriter, r *http.Request, ip, operatorID string) {
	sessionToken, err := token.New()
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.IssueOperatorToken(r.Context(), operatorID, token.Hash(sessionToken)); err != nil {
		writeError(w, err)
		return
	}

	actor, err := s.store.ResolveActor(r.Context(), token.Hash(sessionToken))
	if err != nil {
		writeError(w, err)
		return
	}

	queues, err := s.store.QueuesForActor(r.Context(), actor)
	if err != nil {
		writeError(w, err)
		return
	}

	s.redeemLockout.Reset(ip)

	httpx.JSON(w, http.StatusOK, redeemResponse{
		Role:   actor.Type,
		Token:  sessionToken,
		Queues: queues,
	})
}

// acknowledgeRecoveryCode is the client confirming it has the replacement code
// somewhere the owner can reach it. Only then does the redeemed code stop
// working.
//
// Without this step recovery is a coin flip on the network: rotate in place,
// lose the response to a dropped connection or a closed tab, and the owner is
// holding a dead code with no way back into their own business. Two live codes
// for the length of one screen is the cheaper failure.
func (s *Server) acknowledgeRecoveryCode(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireActor(w, r)
	if !ok {
		return
	}
	if !actor.IsOwner() {
		writeError(w, queue.ErrUnauthorized)
		return
	}

	if err := s.store.AcknowledgeRecoveryCode(r.Context(), actor.OwnerID); err != nil {
		writeError(w, err)
		return
	}
	httpx.NoContent(w)
}

// myQueuesResponse answers "who am I and what can I open" in one request. It is
// what replaces enumerating localStorage keys in the browser: the server knows
// the answer, and the browser only has to hold one token.
type myQueuesResponse struct {
	Role   queue.PrincipalType `json:"role"`
	Queues []queue.QueueCard   `json:"queues"`

	// Archived is what the owner has put away, so it can be brought back.
	// Always empty for an operator: their manager decides what they see.
	Archived []queue.Queue `json:"archived"`

	// DisplayName is what the owner asked to be called. Empty for an
	// operator, whose name lives on the roster.
	DisplayName string `json:"displayName"`
}

func (s *Server) myQueues(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireActor(w, r)
	if !ok {
		return
	}

	queues, err := s.store.QueuesForActor(r.Context(), actor)
	if err != nil {
		writeError(w, err)
		return
	}

	res := myQueuesResponse{Role: actor.Type, Queues: queues, Archived: []queue.Queue{}}
	if actor.IsOwner() {
		if res.Archived, err = s.store.ArchivedQueues(r.Context(), actor.OwnerID); err != nil {
			writeError(w, err)
			return
		}
		if res.DisplayName, err = s.store.OwnerName(r.Context(), actor.OwnerID); err != nil {
			writeError(w, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, res)
}

type updateMeRequest struct {
	DisplayName string `json:"displayName"`
}

type updateMeResponse struct {
	DisplayName string `json:"displayName"`
}

const ownerNameLimit = 60

// updateMe lets an owner say what they are called. Owner-only: an operator's
// name is the manager's to set, from the roster.
func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireActor(w, r)
	if !ok {
		return
	}
	if !actor.IsOwner() {
		writeError(w, queue.ErrUnauthorized)
		return
	}

	var req updateMeRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if len([]rune(name)) > ownerNameLimit {
		writeError(w, invalid("Your name must be 60 characters or fewer."))
		return
	}

	if err := s.store.SetOwnerName(r.Context(), actor.OwnerID, name); err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, updateMeResponse{DisplayName: name})
}

type revokeOthersResponse struct {
	Revoked int `json:"revoked"`
}

// revokeOtherSessions signs out the owner's other devices, keeping the one that
// asked.
//
// It is never run as part of recovery. The common reason to recover is a new
// phone, not a stolen one, and an owner who recovers on the way to work should
// not find the counter tablet signed out when they arrive. Someone who has
// actually lost a device asks for this on purpose.
func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireActor(w, r)
	if !ok {
		return
	}
	if !actor.IsOwner() {
		// An operator cannot manage their own sessions; the owner re-issues
		// their code instead. See the permission table in PLAN.md.
		writeError(w, queue.ErrUnauthorized)
		return
	}

	revoked, err := s.store.RevokeOtherOwnerSessions(r.Context(), actor.OwnerID, token.Hash(bearerToken(r)))
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, revokeOthersResponse{Revoked: revoked})
}
