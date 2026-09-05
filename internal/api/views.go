package api

import (
	"context"
	"time"

	"github.com/vivianobiako/qless/api/internal/queue"
)

// CustomerView is everything one customer's phone needs in a single response:
// the public queue state, their own entry if they have one, and the derived
// numbers. Positions and estimates are computed here rather than in the
// browser so every device shows the same figures.
type CustomerView struct {
	State        queue.PublicState `json:"state"`
	Entry        *queue.Entry      `json:"entry"`
	PeopleAhead  int               `json:"peopleAhead"`
	Estimate     *queue.Estimate   `json:"estimate"`
	JoinEstimate *queue.Estimate   `json:"joinEstimate"`
}

func (s *Server) customerView(ctx context.Context, q queue.Queue, entry *queue.Entry) (CustomerView, error) {
	state, err := s.store.PublicState(ctx, q)
	if err != nil {
		return CustomerView{}, err
	}

	view := CustomerView{
		State:        state,
		Entry:        entry,
		JoinEstimate: queue.EstimateWait(state.WaitingCount, state.ServiceMinutes),
	}

	if entry != nil && entry.Status == queue.EntryWaiting {
		view.PeopleAhead = state.PeopleAhead(entry.Number)
		view.Estimate = queue.EstimateWait(view.PeopleAhead, state.ServiceMinutes)
	}

	return view, nil
}

// WaitingRow is an operator-side queue entry with its own estimate attached.
type WaitingRow struct {
	queue.Entry
	Estimate *queue.Estimate `json:"estimate"`
}

// OperatorView is the dashboard payload. This is the only response shape that
// can carry customer names, and whether it does depends on who asked.
type OperatorView struct {
	Queue        queue.Queue  `json:"queue"`
	Serving      *queue.Entry `json:"serving"`
	Waiting      []WaitingRow `json:"waiting"`
	WaitingCount int          `json:"waitingCount"`

	// Stood down inside the recall window, most recent first. Still theirs to
	// be called back on; after the window they are history only.
	Skipped []queue.Entry `json:"skipped"`

	// Measured is what service has actually taken lately, so settings can
	// show the figure the estimates are using next to the one that was typed.
	Measured queue.ServiceMeasure `json:"measured"`

	// Arrival is how long people have been taking to turn up once called —
	// the number a hold time should be set against.
	Arrival queue.ServiceMeasure `json:"arrival"`

	// LastActivityAt is when anything last happened here, so a dashboard
	// opened the next morning can ask whether to start a new day.
	LastActivityAt *time.Time `json:"lastActivityAt"`

	// ShowsNames says whether this payload carries them, so the screen renders
	// a queue of numbers on purpose rather than a queue of blanks by accident.
	ShowsNames bool `json:"showsNames"`
}

// maySeeNames is the single answer to "does this person get names", asked by
// every surface that could carry one. An owner always may; staff may when their
// queue says so, which defaults to off.
func maySeeNames(q queue.Queue, actor queue.Actor) bool {
	return actor.IsOwner() || q.ShowNamesToOperators
}

// operatorView builds the dashboard for one actor.
//
// Names are removed here, on the way out, rather than left to the caller to
// remember: there are three places a name could reach a screen — this view, the
// realtime frame built from it, and history — and every one of them goes
// through a function that takes an actor.
func (s *Server) operatorView(
	ctx context.Context,
	q queue.Queue,
	withNames bool,
) (OperatorView, error) {
	entries, err := s.store.ListActiveEntries(ctx, q.ID)
	if err != nil {
		return OperatorView{}, err
	}

	measured, err := s.store.MeasuredService(ctx, q.ID)
	if err != nil {
		return OperatorView{}, err
	}
	serviceMinutes := q.ServiceMinutesIn(measured)

	arrival, err := s.store.MeasuredArrival(ctx, q.ID)
	if err != nil {
		return OperatorView{}, err
	}

	lastActivity, err := s.store.LastActivity(ctx, q.ID)
	if err != nil {
		return OperatorView{}, err
	}

	view := OperatorView{
		Queue:          q,
		Waiting:        []WaitingRow{},
		ShowsNames:     withNames,
		Measured:       measured,
		Arrival:        arrival,
		LastActivityAt: lastActivity,
	}

	for _, entry := range entries {
		if !withNames {
			entry.CustomerName = ""
		}

		if entry.Status == queue.EntryServing {
			serving := entry
			view.Serving = &serving
			continue
		}
		view.Waiting = append(view.Waiting, WaitingRow{
			Entry:    entry,
			Estimate: queue.EstimateWait(len(view.Waiting), serviceMinutes),
		})
	}

	view.WaitingCount = len(view.Waiting)

	skipped, err := s.store.ListRecentlySkipped(ctx, q.ID)
	if err != nil {
		return OperatorView{}, err
	}
	if !withNames {
		for i := range skipped {
			skipped[i].CustomerName = ""
		}
	}
	view.Skipped = skipped

	return view, nil
}
