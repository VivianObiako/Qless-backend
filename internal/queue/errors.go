package queue

import "errors"

// Domain errors. The HTTP layer maps each of these to a status code and a
// message written for a customer standing in a shop, never a raw driver error.
var (
	ErrNotFound      = errors.New("queue not found")
	ErrEntryNotFound = errors.New("entry not found")
	ErrQueuePaused   = errors.New("queue is paused")
	ErrQueueClosed   = errors.New("queue is closed")
	ErrQueueFull     = errors.New("queue is full")
	ErrAlreadyJoined = errors.New("already in this queue")
	ErrNotInQueue    = errors.New("no active entry in this queue")

	// ErrEntryNotActive is a stale dashboard, not a broken one: the operator
	// clicked a row that someone else had already dealt with.
	ErrEntryNotActive = errors.New("entry is no longer active")

	// ErrRecallExpired is a skipped customer whose window has closed: the
	// record stays, but the number is no longer theirs to be called back on.
	ErrRecallExpired = errors.New("too long since this customer was skipped")
	ErrUnauthorized  = errors.New("not authorized to operate this queue")
	ErrInvalidInput  = errors.New("invalid input")

	// ErrInvalidCode is the single answer redeem gives to every failure: a code
	// that never existed, one that has been rotated away, and one belonging to a
	// revoked operator are indistinguishable from outside.
	ErrInvalidCode = errors.New("invalid access code")

	ErrOperatorNotFound = errors.New("operator not found")

	ErrSeatNotFound = errors.New("seat not found")

	// ErrSeatClosed is a call aimed at a chair that is not in service; the
	// counter should not have offered it.
	ErrSeatClosed = errors.New("seat is closed")

	// ErrNoFreeSeat is a call with no seat named on a queue where every open
	// seat has somebody on it. With one seat the call reuses it; with several
	// the caller has to say which person to stand down.
	ErrNoFreeSeat = errors.New("every seat is taken")
)
