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
			wantLabel:   "5–5 min",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := queue.EstimateWait(tc.peopleAhead, tc.serviceMins)

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
