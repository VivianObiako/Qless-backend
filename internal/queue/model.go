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

	// HoldMinutes is how long a called customer's place is held: the counter
	// suggests a skip after it, a skipped number can be recalled within it,
	// and the pass promises it. Zero means no hold at all.
	HoldMinutes int `json:"holdMinutes"`

	// PauseNote is shown to customers while the queue is paused, and cleared
	// when it resumes.
	PauseNote string `json:"pauseNote"`

	// ArchivedAt is set once the owner has put the queue away. It is hidden
	// from their list and refuses joins, and everything it recorded stays.
	ArchivedAt *time.Time `json:"archivedAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RecallWindow is how long a skipped customer can be called back with the
// number they had. It is the queue's hold time; zero means a skip is final.
func (q Queue) RecallWindow() time.Duration {
	return time.Duration(q.HoldMinutes) * time.Minute
}

// MeasureSample is how many real service times a queue needs in the recent
// window before the measured average replaces the owner's setting.
const MeasureSample = 5

// ServiceMeasure is what the day has actually looked like: the average of the
// last few real start-to-finish times, and how many of them there were.
type ServiceMeasure struct {
	Minutes int `json:"minutes"`
	Sample  int `json:"sample"`
}

// ServiceMinutesIn is the figure every estimate is built from: the measured
// average once the day has produced enough of one, the owner's setting until
// then. A range that is wrong by noon teaches customers to ignore it.
func (q Queue) ServiceMinutesIn(m ServiceMeasure) int {
	if m.Sample >= MeasureSample && m.Minutes > 0 {
		return m.Minutes
	}
	return q.AverageServiceMinutes
}

// QueueCard is a queue with the two live figures an owner reads a list by:
// what is being served and how many are waiting.
type QueueCard struct {
	Queue
	ServingNumber *int `json:"servingNumber"`
	WaitingCount  int  `json:"waitingCount"`
}

// Presence is what a customer has told the counter about where they are.
// It is about this visit, so it lives on the entry and a new number starts
// with nothing said.
type Presence string

const (
	PresenceOnTheWay Presence = "ON_THE_WAY"
	PresenceHere     Presence = "HERE"
	PresenceHold     Presence = "HOLD"
)

func (p Presence) Valid() bool {
	return p == PresenceOnTheWay || p == PresenceHere || p == PresenceHold
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

	// Nil until the customer says something. Never on a public surface:
	// PublicState carries numbers, and this rides on entries only.
	Presence   *Presence  `json:"presence"`
	PresenceAt *time.Time `json:"presenceAt"`

	// Added at the counter by staff rather than from a phone. Nobody can
	// recover this entry on a device, so the counter says so.
	WalkIn bool `json:"walkIn"`
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
	HoldMinutes           int    `json:"holdMinutes"`
	PauseNote             string `json:"pauseNote"`
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
		HoldMinutes:           q.HoldMinutes,
		PauseNote:             q.PauseNote,
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

	// ServiceMinutes is the figure the estimates below were built from — the
	// measured average once there is one, the setting until then.
	ServiceMinutes int `json:"serviceMinutes"`

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
