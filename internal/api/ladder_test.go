package api

import (
	"strings"
	"testing"

	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
)

// The push ladder ranks on turns: with three chairs open, two people ahead
// is "you're next" and eleven is still "getting close", where one chair
// would have said "waiting" for both.
func TestRungForRanksOnTurnsNotPeople(t *testing.T) {
	waiting := []int{}
	for n := 1; n <= 20; n++ {
		waiting = append(waiting, n)
	}
	for _, tc := range []struct {
		seats  int
		number int
		want   int
	}{
		{seats: 1, number: 1, want: rungNext},
		{seats: 1, number: 3, want: rungClose},
		{seats: 1, number: 4, want: rungClose},
		{seats: 1, number: 5, want: rungWaiting},
		{seats: 3, number: 3, want: rungNext},
		{seats: 3, number: 4, want: rungClose},
		{seats: 3, number: 12, want: rungClose},
		{seats: 3, number: 13, want: rungWaiting},
	} {
		state := queue.PublicState{WaitingNumbers: waiting, OpenSeats: tc.seats}
		target := storage.PushTarget{Number: tc.number, Status: queue.EntryWaiting}
		if got, _ := rungFor(target, state); got != tc.want {
			t.Errorf("#%d with %d seats open: rung %d, want %d", tc.number, tc.seats, got, tc.want)
		}
	}

	served := storage.PushTarget{Number: 1, Status: queue.EntryServing}
	if got, _ := rungFor(served, queue.PublicState{OpenSeats: 3}); got != rungCurrent {
		t.Errorf("somebody being served is rung %d, want current", got)
	}
}

// A draw has no "getting close": a phone hears nothing until its number is
// drawn, then that it is next, then that it is its turn. Being the lowest
// number counts for nothing.
func TestRungForInADraw(t *testing.T) {
	drawn := 7
	state := queue.PublicState{
		Queue:          queue.Summary{ServingOrder: queue.ServingRandom},
		WaitingNumbers: []int{1, 2, 3, 7, 9},
		OpenSeats:      1,
		UpNextNumber:   &drawn,
	}

	for _, tc := range []struct {
		number int
		want   int
	}{
		{number: 7, want: rungNext},
		{number: 1, want: rungWaiting},
		{number: 2, want: rungWaiting},
		{number: 9, want: rungWaiting},
	} {
		target := storage.PushTarget{Number: tc.number, Status: queue.EntryWaiting}
		if got, _ := rungFor(target, state); got != tc.want {
			t.Errorf("#%d in a draw: rung %d, want %d", tc.number, got, tc.want)
		}
	}

	served := storage.PushTarget{Number: 4, Status: queue.EntryServing}
	if got, _ := rungFor(served, state); got != rungCurrent {
		t.Errorf("somebody called in a draw is rung %d, want current", got)
	}

	state.UpNextNumber = nil
	if got, _ := rungFor(storage.PushTarget{Number: 1, Status: queue.EntryWaiting}, state); got != rungWaiting {
		t.Errorf("before anything is drawn the lowest number is rung %d, want waiting", got)
	}
}

// The people in a queue are called what the owner calls them.
func TestMessagesUseTheQueuesNouns(t *testing.T) {
	q := queue.Queue{Name: "Hack Day", Slug: "hack", PersonNoun: "team", PeopleNoun: "teams"}

	several := messageFor(rungClose, 14, 3, q, "Counter", 1, "https://web")
	if !strings.Contains(several.Body, "3 teams ahead") {
		t.Errorf("close body = %q, want the plural noun", several.Body)
	}
	one := messageFor(rungClose, 14, 1, q, "Counter", 1, "https://web")
	if !strings.Contains(one.Body, "One team ahead") {
		t.Errorf("close body = %q, want the singular noun", one.Body)
	}

	q.ServingOrder = queue.ServingRandom
	next := messageFor(rungNext, 14, 0, q, "Counter", 1, "https://web")
	if !strings.Contains(next.Body, "Get ready.") {
		t.Errorf("drawn body = %q, want get ready", next.Body)
	}
}

// The turn message keeps "the counter" for a one-seat queue and names the
// chair once there is more than one to choose from.
func TestTurnMessageNamesTheChair(t *testing.T) {
	q := queue.Queue{Name: "Ade's", Slug: "ade"}

	one := messageFor(rungCurrent, 14, 0, q, "Counter", 1, "https://web")
	if !strings.Contains(one.Body, "Head to the counter.") {
		t.Errorf("one-seat body = %q, want the counter", one.Body)
	}

	many := messageFor(rungCurrent, 14, 0, q, "Chair 2", 3, "https://web")
	if !strings.Contains(many.Body, "Go to Chair 2.") {
		t.Errorf("many-seat body = %q, want the chair named", many.Body)
	}
}
