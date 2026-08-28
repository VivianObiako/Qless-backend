package queue

// PrincipalType names what kind of thing is holding a session token. It mirrors
// the principal_type enum in the database.
type PrincipalType string

const (
	PrincipalOwner    PrincipalType = "OWNER"
	PrincipalOperator PrincipalType = "OPERATOR"
)

// Actor is whoever is making the request, resolved from their session token.
//
// ID is the principal's own id; OwnerID is the business they act within, which
// for an owner is the same value. Every authorization question is asked of an
// Actor rather than of a token, so the answer for a queue can come from more
// than one direction — an owner holding it directly today, an operator assigned
// to it from 00003, a service grouping later — without any caller changing.
type Actor struct {
	Type    PrincipalType
	ID      string
	OwnerID string
}

func (a Actor) IsOwner() bool {
	return a.Type == PrincipalOwner
}

