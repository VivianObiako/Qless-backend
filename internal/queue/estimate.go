package queue

import (
	"fmt"
	"math"
)

// Estimate is always a range. Qless never presents a wait as an exact promise.
type Estimate struct {
	LowMinutes  int    `json:"lowMinutes"`
	HighMinutes int    `json:"highMinutes"`
	Label       string `json:"label"`
}

// EstimateWait returns the wait for a customer with peopleAhead customers in
// front of them, or nil when they are next or already being served — at that
// point the state itself is the message, not a duration.
//
// Computed server-side so that every client agrees on the number.
func EstimateWait(peopleAhead, averageServiceMinutes int) *Estimate {
	if peopleAhead <= 0 || averageServiceMinutes <= 0 {
		return nil
	}

	base := float64(peopleAhead * averageServiceMinutes)
	low := roundToFive(base * 0.8)
	high := roundToFive(base * 1.2)

	if low < 5 {
		low = 5
	}
	if high < low {
		high = low
	}

	return &Estimate{
		LowMinutes:  low,
		HighMinutes: high,
		Label:       formatRange(low, high),
	}
}

// EstimateTable returns the estimate for every position this queue currently
// has, indexed by people ahead: index 0 is whoever is next and carries no
// estimate, and the final element is what a customer joining right now would
// wait.
//
// It exists so realtime clients never recompute the formula. A customer's
// position is derived in the browser — that is what keeps other customers'
// names off the wire — but the wait itself stays server-side, where one
// implementation guarantees every screen agrees.
func EstimateTable(waitingCount, averageServiceMinutes int) []*Estimate {
	table := make([]*Estimate, waitingCount+1)
	for ahead := range table {
		table[ahead] = EstimateWait(ahead, averageServiceMinutes)
	}
	return table
}

func roundToFive(minutes float64) int {
	return int(math.Round(minutes/5) * 5)
}

// formatRange keeps the unit out of the middle of the range: short waits read
// "10–20 min", long ones switch to hours as "1h 50m – 2h 15m".
func formatRange(low, high int) string {
	if high < 90 {
		return fmt.Sprintf("%d–%d min", low, high)
	}
	return fmt.Sprintf("%s – %s", formatDuration(low), formatDuration(high))
}

// formatDuration renders plain minutes below an hour and hours beyond that, so
// a long wait reads as "2h 15m" rather than "135 min".
func formatDuration(minutes int) string {
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	hours := minutes / 60
	rem := minutes % 60
	if rem == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, rem)
}

