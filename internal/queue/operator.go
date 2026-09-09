package queue

import "time"

type OperatorStatus string

const (
	OperatorActive  OperatorStatus = "ACTIVE"
	OperatorRevoked OperatorStatus = "REVOKED"
)

// Operator is a named person who works the counter. They belong to one owner,
// reach only the queues assigned to them, and hold an access code that the
// owner can regenerate or take away at any time.
//
// A revoked operator is kept rather than deleted, so entries they handled keep
// resolving to a name.
type Operator struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"displayName"`
	Status      OperatorStatus `json:"status"`
	QueueIDs    []string       `json:"queueIds"`

	// Seats are the chairs this person holds, at most one per queue.
	Seats []OperatorSeat `json:"seats"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// OperatorSeat is one chair an operator holds, named for the roster.
type OperatorSeat struct {
	QueueID  string `json:"queueId"`
	SeatID   string `json:"seatId"`
	SeatName string `json:"seatName"`
}

func (o Operator) IsActive() bool {
	return o.Status == OperatorActive
}

// ActedBy names who moved an entry. Nil on entries that ended by the customer's
// own hand, and on everything that happened before operators existed.
type ActedBy struct {
	Type PrincipalType `json:"type"`
	// OperatorName is empty for an owner, who has no name to show. A revoked
	// operator still has one, which is the whole reason revoking is soft.
	OperatorName string `json:"operatorName,omitempty"`
}

// HistoryEntry is a finished entry with the person who dealt with it attached.
type HistoryEntry struct {
	Entry
	ActedBy *ActedBy `json:"actedBy"`
}
