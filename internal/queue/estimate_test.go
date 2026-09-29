package queue_test

import (
	"testing"

	"github.com/vivianobiako/qless/api/internal/queue"
)

func TestEstimateWait(t *testing.T) {
	for _, tc := range []struct {
		name        string
		peopleAhead int
		serviceMins int
		openSeats   int
		wantNil     bool
		wantLow     int
		wantHigh    int
		wantLabel   string
	}{
		{
			name:        "nobody ahead has no estimate, the state is the message",
			peopleAhead: 0,
			serviceMins: 15,
			wantNil:     true,
		},
		{
			name:        "one ahead",
			peopleAhead: 1,
			serviceMins: 15,
			wantLow:     10,
			wantHigh:    20,
			wantLabel:   "10–20 min",
		},
		{
			name:        "three ahead",
			peopleAhead: 3,
			serviceMins: 15,
			wantLow:     35,
			wantHigh:    55,
			wantLabel:   "35–55 min",
		},
		{
			name:        "six ahead crosses into hours",
			peopleAhead: 6,
			serviceMins: 15,
			wantLow:     70,
			wantHigh:    110,
			wantLabel:   "1h 10m – 1h 50m",
		},
		{
			name:        "a very short service time still floors at five minutes",
			peopleAhead: 1,
			serviceMins: 2,
			wantLow:     5,
			wantHigh:    5,
			wantLabel:   "5 min",
		},
		{
			name:        "both ends rounding to the same figure read as one",
			peopleAhead: 2,
			serviceMins: 5,
			wantLow:     10,
			wantHigh:    10,
			wantLabel:   "10 min",
		},
		{
			name:        "three chairs make three ahead one turn",
			peopleAhead: 3,
			serviceMins: 15,
			openSeats:   3,
			wantLow:     10,
			wantHigh:    20,
			wantLabel:   "10–20 min",
		},
		{
			name:        "three chairs make four ahead two turns",
			peopleAhead: 4,
			serviceMins: 15,
			openSeats:   3,
			wantLow:     25,
			wantHigh:    35,
			wantLabel:   "25–35 min",
		},
		{
			name:        "every chair closed still counts as one",
			peopleAhead: 3,
			serviceMins: 15,
			openSeats:   0,
			wantLow:     35,
			wantHigh:    55,
			wantLabel:   "35–55 min",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seats := tc.openSeats
			if seats == 0 && tc.name != "every chair closed still counts as one" {
				seats = 1
			}
			got := queue.EstimateWait(tc.peopleAhead, tc.serviceMins, seats)

			if tc.wantNil {
				if got != nil {
					t.Fatalf("estimate = %+v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("estimate = nil, want a range")
			}
			if got.LowMinutes != tc.wantLow || got.HighMinutes != tc.wantHigh {
				t.Errorf("range = %d–%d, want %d–%d", got.LowMinutes, got.HighMinutes, tc.wantLow, tc.wantHigh)
			}
			if got.Label != tc.wantLabel {
				t.Errorf("label = %q, want %q", got.Label, tc.wantLabel)
			}
		})
	}
}

func TestPeopleAheadCountsLowerNumbersOnly(t *testing.T) {
	state := queue.PublicState{WaitingNumbers: []int{4, 5, 9, 12}}

	for _, tc := range []struct {
		number int
		want   int
	}{
		{number: 4, want: 0},
		{number: 9, want: 2},
		{number: 12, want: 3},
		{number: 20, want: 4},
	} {
		if got := state.PeopleAhead(tc.number); got != tc.want {
			t.Errorf("people ahead of #%d = %d, want %d", tc.number, got, tc.want)
		}
	}
}

func TestTurnsAheadDividesByOpenSeats(t *testing.T) {
	for _, tc := range []struct{ ahead, seats, want int }{
		{ahead: 0, seats: 1, want: 0},
		{ahead: 2, seats: 1, want: 2},
		{ahead: 2, seats: 3, want: 0},
		{ahead: 3, seats: 3, want: 1},
		{ahead: 11, seats: 3, want: 3},
		{ahead: 12, seats: 3, want: 4},
		{ahead: 4, seats: 0, want: 4},
	} {
		if got := queue.TurnsAhead(tc.ahead, tc.seats); got != tc.want {
			t.Errorf("turns ahead with %d ahead and %d seats = %d, want %d", tc.ahead, tc.seats, got, tc.want)
		}
	}
}
