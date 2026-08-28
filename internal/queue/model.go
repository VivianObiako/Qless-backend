// Package queue holds the Qless domain types and the pure logic that operates
// on them. It has no knowledge of HTTP or SQL.
package queue

import "time"

type Status string

const (
	StatusOpen   Status = "OPEN"
	StatusPaused Status = "PAUSED"
	StatusClosed Status = "CLOSED"
)

type EntryStatus string

const (
	EntryWaiting  EntryStatus = "WAITING"
	EntryServing  EntryStatus = "SERVING"
	EntryAttended EntryStatus = "ATTENDED"
	EntrySkipped  EntryStatus = "SKIPPED"
	EntryLeft     EntryStatus = "LEFT"
	EntryCleared  EntryStatus = "CLEARED"
)

// IsActive reports whether an entry still occupies a place in the queue.
// Exactly these two statuses are covered by the one_active_entry_per_token
// index, which is what allows a skipped or departed customer to rejoin.
func (s EntryStatus) IsActive() bool {
	return s == EntryWaiting || s == EntryServing
}

type Queue struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	Description           string `json:"description"`
	AverageServiceMinutes int    `json:"averageServiceMinutes"`
	MaxCapacity           *int   `json:"maxCapacity"`
	Status                Status `json:"status"`
	NextNumber            int    `json:"nextNumber"`

	// ShowNamesToOperators is settable from here on; phase 5 is what makes it
	// change any payload. It never reaches a public surface — Summary is what
	// customers see, and it does not carry this.
	ShowNamesToOperators bool `json:"showNamesToOperators"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Entry struct {
	ID           string      `json:"id"`
	QueueID      string      `json:"queueId"`
	Number       int         `json:"number"`
	CustomerName string      `json:"customerName"`
	Status       EntryStatus `json:"status"`
	JoinedAt     time.Time   `json:"joinedAt"`
	StartedAt    *time.Time  `json:"startedAt"`
	CompletedAt  *time.Time  `json:"completedAt"`
}

// Summary is the queue metadata safe to expose on public surfaces.
type Summary struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	Description           string `json:"description"`
	Status                Status `json:"status"`
	AverageServiceMinutes int    `json:"averageServiceMinutes"`
	MaxCapacity           *int   `json:"maxCapacity"`
}

func (q Queue) Summary() Summary {
	return Summary{
		ID:                    q.ID,
		Name:                  q.Name,
		Slug:                  q.Slug,
		Description:           q.Description,
		Status:                q.Status,
		AverageServiceMinutes: q.AverageServiceMinutes,
		MaxCapacity:           q.MaxCapacity,
	}
}

// PublicState is what every customer browser and display screen receives.
// It deliberately carries no names: a customer knows their own number from
// /me and derives their position from WaitingNumbers locally, so one client
// never learns another customer's identity.
type PublicState struct {
	Queue          Summary `json:"queue"`
	ServingNumber  *int    `json:"servingNumber"`
	WaitingNumbers []int   `json:"waitingNumbers"`
	WaitingCount   int     `json:"waitingCount"`
	IsFull         bool    `json:"isFull"`

	// Estimates is indexed by people ahead, so a customer who has worked out
	// their own position from WaitingNumbers can read their wait without the
	// server having to know which entry is asking. See EstimateTable.
	Estimates []*Estimate `json:"estimates"`
}

// PeopleAhead counts the waiting customers with a lower number. Waiting order
// is always by number ascending; serving a specific customer pulls them out
// without reordering anyone else.
func (p PublicState) PeopleAhead(number int) int {
	ahead := 0
	for _, n := range p.WaitingNumbers {
		if n < number {
			ahead++
		}
	}
	return ahead
}

