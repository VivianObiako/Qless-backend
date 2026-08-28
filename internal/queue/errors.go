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
	ErrUnauthorized   = errors.New("not authorized to operate this queue")
	ErrInvalidInput   = errors.New("invalid input")

	// ErrInvalidCode is the single answer redeem gives to every failure: a code
	// that never existed, one that has been rotated away, and one belonging to a
	// revoked operator are indistinguishable from outside.
	ErrInvalidCode = errors.New("invalid access code")

	ErrOperatorNotFound = errors.New("operator not found")
)

