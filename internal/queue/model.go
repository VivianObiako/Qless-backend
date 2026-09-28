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

// ServingOrder is how the counter picks who is called next. In order is the
// queue as it has always been: the lowest waiting number. Random is a draw:
// everyone holds a number, the counter calls one at random, and one more is
// drawn ahead as "up next" so a called person had some warning.
type ServingOrder string

const (
	ServingInOrder ServingOrder = "IN_ORDER"
	ServingRandom  ServingOrder = "RANDOM"
)

func (o ServingOrder) Valid() bool {
	return o == ServingInOrder || o == ServingRandom
}

// CallPhrase is what the moment of being called is named on the screens the
// room sees: "Now serving" at a counter, "Now presenting" at a hackathon,
// "Now seeing" at a clinic, "Now up" for anything else. It is a short list
// rather than a free word because the phrase appears in several forms
// ("Now presenting", "Thanks for presenting") that a typed word cannot be
// bent into reliably; the web app holds the wording for each.
type CallPhrase string

const (
	CallServing    CallPhrase = "SERVING"
	CallPresenting CallPhrase = "PRESENTING"
	CallSeeing     CallPhrase = "SEEING"
	CallUp         CallPhrase = "UP"
)

func (p CallPhrase) Valid() bool {
	return p == CallServing || p == CallPresenting || p == CallSeeing || p == CallUp
}

// What the people in a queue are called unless the owner says otherwise, and
// the longest word the columns accept.
const (
	DefaultPersonNoun = "customer"
	DefaultPeopleNoun = "customers"
	NounLimit         = 30
)

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

	// SeatsFixed means staff work the chair the owner assigned and cannot
	// pick another; off, they may take any free chair and leave it.
	SeatsFixed bool `json:"seatsFixed"`

	// ServingOrder is how the counter picks who is next. A draw needs a
	// MaxCapacity: the places are the point, and the database refuses the
	// combination without one.
	ServingOrder ServingOrder `json:"servingOrder"`

	// PersonNoun and PeopleNoun are what the people in this queue are called
	// on their phones and on the wall: customer and customers, guest and
	// guests, participant and participants.
	PersonNoun string `json:"personNoun"`
	PeopleNoun string `json:"peopleNoun"`

	// CallPhrase is what being called is named on the wall, the join page
	// and the pass. Serving unless the owner picks another.
	CallPhrase CallPhrase `json:"callPhrase"`

	// ResetAt is when the numbering last started again; nil for a queue
	// never reset. A draw counts its places from here, so a number from a
	// previous event does not take a place at this one.
	ResetAt *time.Time `json:"resetAt"`

	// ArchivedAt is set once the owner has put the queue away. It is hidden
	// from their list and refuses joins, and everything it recorded stays.
	ArchivedAt *time.Time `json:"archivedAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// IsDraw reports whether this queue calls people at random.
func (q Queue) IsDraw() bool {
	return q.ServingOrder == ServingRandom
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

// SeatMeasure is one chair's measured service: what serving has taken
// there lately, for an owner comparing chairs.
type SeatMeasure struct {
	SeatID   string `json:"seatId"`
	SeatName string `json:"seatName"`
	Minutes  int    `json:"minutes"`
	Sample   int    `json:"sample"`
}

// QueueCard is a queue with the two live figures an owner reads a list by:
// what is being served and how many are waiting.
type QueueCard struct {
	Queue
	// ServingNumber is the most recent call, kept for the one-seat card;
	// ServingCount of OpenSeats is what a card with chairs reads.
	ServingNumber *int `json:"servingNumber"`
	ServingCount  int  `json:"servingCount"`
	OpenSeats     int  `json:"openSeats"`
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

	// ServedAt is when service began, as distinct from StartedAt, which is
	// when the number was called. Nil until inferred or tapped; the gap is
	// the customer walking back.
	ServedAt *time.Time `json:"servedAt"`

	// Nil until the customer says something. Never on a public surface:
	// PublicState carries numbers, and this rides on entries only.
	Presence   *Presence  `json:"presence"`
	PresenceAt *time.Time `json:"presenceAt"`

	// Added at the counter by staff rather than from a phone. Nobody can
	// recover this entry on a device, so the counter says so.
	WalkIn bool `json:"walkIn"`

	// SeatID is where they were called to. Nil while they wait, set by the
	// call and kept afterwards so history can say which chair served them.
	SeatID *string `json:"seatId"`

	// DrawnAt is when a draw picked this number as up next. Nil in a queue
	// served in order, and until the draw reaches them in one served at
	// random. Kept after the call, so history can say when they were drawn.
	DrawnAt *time.Time `json:"drawnAt"`
}

// Seat is one place a customer is sent to be served: a chair, a counter, an
// exam room. A queue with one seat is a queue with a counter, and every
// queue has at least one. Active is whether it is open for service right
// now; RemovedAt is set on a seat the owner has taken away, which stays so
// history can still name it.
type Seat struct {
	ID        string     `json:"id"`
	QueueID   string     `json:"queueId"`
	Name      string     `json:"name"`
	Position  int        `json:"position"`
	Active    bool       `json:"active"`
	RemovedAt *time.Time `json:"removedAt"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`

	// Worker is who is at this chair: an operator, the owner, or nobody.
	Worker *SeatWorker `json:"worker"`
}

// SeatWorker names who works a seat. OperatorID is empty for the owner;
// Name is what the tile shows, the operator's display name or the owner's.
type SeatWorker struct {
	Type       PrincipalType `json:"type"`
	OperatorID string        `json:"operatorId,omitempty"`
	Name       string        `json:"name"`
}

// WorkedBy reports whether this actor is the one at the seat.
func (s Seat) WorkedBy(actor Actor) bool {
	if s.Worker == nil {
		return false
	}
	if actor.IsOwner() {
		return s.Worker.Type == PrincipalOwner
	}
	return s.Worker.Type == PrincipalOperator && s.Worker.OperatorID == actor.ID
}

// PublicSeat is what a customer surface knows about a seat: enough to say
// "Go to Chair 2" and to show a closed chair as closed. Seat names are
// public the moment they are on a pass, which the settings say.
type PublicSeat struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`

	// WorkerName is who is at the chair, so the pass can say "Kofi is ready
	// for you". Empty when nobody is, or the owner has given no name. The
	// owner names staff on the roster knowing it reaches the pass.
	WorkerName string `json:"workerName"`
}

func (s Seat) Public() PublicSeat {
	public := PublicSeat{ID: s.ID, Name: s.Name, Active: s.Active}
	if s.Worker != nil {
		public.WorkerName = s.Worker.Name
	}
	return public
}

// ServingSlot is one number being served and where: the wall shows the
// number under the chair, the pass tells its holder which chair to go to.
type ServingSlot struct {
	Number   int    `json:"number"`
	SeatID   string `json:"seatId"`
	SeatName string `json:"seatName"`
}

// TurnsAhead is how many calls have to happen before a waiting customer's
// own, with several seats calling at once: three chairs and three people
// ahead is one turn, not three. Both ladders rank on this rather than on
// people, or a customer hears "you're next" and "it's your turn" a second
// apart. A queue with every seat closed still counts as one.
func TurnsAhead(peopleAhead, openSeats int) int {
	if openSeats < 1 {
		openSeats = 1
	}
	return peopleAhead / openSeats
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

	// ServingOrder is public because the pass, the wall and the join page
	// all change their wording on it: a draw has no queue to be ahead in.
	ServingOrder ServingOrder `json:"servingOrder"`
	PersonNoun   string       `json:"personNoun"`
	PeopleNoun   string       `json:"peopleNoun"`
	CallPhrase   CallPhrase   `json:"callPhrase"`
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
		ServingOrder:          q.ServingOrder,
		PersonNoun:            q.PersonNoun,
		PeopleNoun:            q.PeopleNoun,
		CallPhrase:            q.CallPhrase,
	}
}

// IsDraw reports whether this queue calls people at random.
func (s Summary) IsDraw() bool {
	return s.ServingOrder == ServingRandom
}

// PublicState is what every customer browser and display screen receives.
// It deliberately carries no names: a customer knows their own number from
// /me and derives their position from WaitingNumbers locally, so one client
// never learns another customer's identity.
type PublicState struct {
	Queue Summary `json:"queue"`

	// ServingNumber is the number called most recently. It stays so a board
	// from before seats keeps working; Serving is the whole picture.
	ServingNumber *int `json:"servingNumber"`

	// Serving is every number being served and the seat it is at, in seat
	// order. Seats are the queue's seats in order, closed ones included and
	// removed ones left out; OpenSeats is how many are in service, which is
	// what the estimate and the ladder divide by.
	Serving   []ServingSlot `json:"serving"`
	Seats     []PublicSeat  `json:"seats"`
	OpenSeats int           `json:"openSeats"`

	WaitingNumbers []int `json:"waitingNumbers"`
	WaitingCount   int   `json:"waitingCount"`
	IsFull         bool  `json:"isFull"`

	// UpNextNumber is the number a draw has picked to be called next. Nil in
	// a queue served in order, where WaitingNumbers already says who is
	// next, and in a draw before the first call or once the pool is empty.
	UpNextNumber *int `json:"upNextNumber"`

	// PlacesTaken is what the capacity is measured against: the people in
	// line in a queue served in order, every number handed out since the
	// last reset in a draw. With no capacity it is still reported.
	PlacesTaken int `json:"placesTaken"`

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
