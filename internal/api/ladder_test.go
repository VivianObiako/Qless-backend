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
