package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
)

func newDrawQueue(t *testing.T, store *storage.Store, capacity int) queue.Queue {
	t.Helper()

	q, err := store.CreateQueue(context.Background(), storage.CreateQueueParams{
		Name:                     t.Name(),
		AverageServiceMinutes:    15,
		MaxCapacity:              &capacity,
		ServingOrder:             queue.ServingRandom,
		NewOwnerRecoveryCodeHash: token.Hash("recovery-" + t.Name() + "-" + freshSecret(t)),
		NewOwnerTokenHash:        token.Hash("owner-" + t.Name() + "-" + freshSecret(t)),
	})
	if err != nil {
		t.Fatalf("create draw queue: %v", err)
	}
	return q.Queue
}

// publicState reads the queue afresh first: reset and settings change the
// row, and the state is built from it.
func publicState(t *testing.T, store *storage.Store, queueID string) queue.PublicState {
	t.Helper()

	ctx := context.Background()
	q, err := store.GetQueue(ctx, queueID)
	if err != nil {
		t.Fatalf("get queue: %v", err)
	}
	state, err := store.PublicState(ctx, q)
	if err != nil {
		t.Fatalf("public state: %v", err)
	}
	return state
}

func upNext(t *testing.T, store *storage.Store, queueID string) int {
	t.Helper()

	state := publicState(t, store, queueID)
	if state.UpNextNumber == nil {
		return 0
	}
	return *state.UpNextNumber
}

func serveNext(t *testing.T, store *storage.Store, queueID string) int {
	t.Helper()

	result, err := store.ServeNext(context.Background(), queueID, "", testOwner)
	if err != nil {
		t.Fatalf("serve next: %v", err)
	}
	if result.Served == nil {
		return 0
	}
	return result.Served.Number
}

func entryByNumber(t *testing.T, entries []queue.Entry, number int) queue.Entry {
	t.Helper()

	for _, entry := range entries {
		if entry.Number == number {
			return entry
		}
	}
	t.Fatalf("no entry numbered %d", number)
	return queue.Entry{}
}

// The whole draw, start to finish: nothing is drawn until the first call,
// each call takes the number drawn before it and draws another, and every
// number is called exactly once.
func TestDrawCallsTheNumberDrawnAhead(t *testing.T) {
	store := newTestStore(t)
	q := newDrawQueue(t, store, 10)

	const people = 6
	for i := range people {
		join(t, store, q.ID, fmt.Sprintf("team-%d", i))
	}

	if got := upNext(t, store, q.ID); got != 0 {
		t.Fatalf("up next before the first call = %d, want nothing drawn", got)
	}

	called := map[int]bool{}
	first := serveNext(t, store, q.ID)
	called[first] = true

	for range people - 1 {
		drawn := upNext(t, store, q.ID)
		if drawn == 0 {
			t.Fatal("nothing drawn while people are still waiting")
		}
		if called[drawn] {
			t.Fatalf("drew %d, who has already been called", drawn)
		}

		got := serveNext(t, store, q.ID)
		if got != drawn {
			t.Fatalf("called %d, want the drawn number %d", got, drawn)
		}
		called[got] = true
	}

	if len(called) != people {
		t.Fatalf("called %d different numbers, want %d", len(called), people)
	}
	if got := upNext(t, store, q.ID); got != 0 {
		t.Fatalf("up next with nobody waiting = %d, want nothing", got)
	}
}

// The point of a draw. Twelve people called in number order by chance is
// one in 479 million, so a failure here is a bug, not bad luck.
func TestDrawIsNotNumberOrder(t *testing.T) {
	store := newTestStore(t)
	q := newDrawQueue(t, store, 20)

	const people = 12
	for i := range people {
		join(t, store, q.ID, fmt.Sprintf("team-%d", i))
	}

	ascending := true
	previous := 0
	for range people {
		number := serveNext(t, store, q.ID)
		if number < previous {
			ascending = false
		}
		previous = number
	}
	if ascending {
		t.Fatal("twelve numbers were called in ascending order: the draw is not random")
	}
}

func TestDrawStateQuotesNoWait(t *testing.T) {
	store := newTestStore(t)
	q := newDrawQueue(t, store, 10)

	for i := range 3 {
		join(t, store, q.ID, fmt.Sprintf("team-%d", i))
	}
	serveNext(t, store, q.ID)

	state := publicState(t, store, q.ID)
	if len(state.Estimates) != 0 {
		t.Errorf("estimates = %d rows, want none in a draw", len(state.Estimates))
	}
	if state.WaitingCount != 2 || len(state.WaitingNumbers) != 2 {
		t.Errorf("waiting = %d %v, want the two not yet called", state.WaitingCount, state.WaitingNumbers)
	}
	if !state.Queue.IsDraw() {
		t.Error("the public summary does not say this is a draw")
	}
}

// Whatever empties the up-next slot, somebody else is drawn into it in the
// same transaction, so the wall never shows a gap while people wait.
func TestDrawRefillsWhenTheDrawnNumberGoes(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		vacate func(store *storage.Store, q queue.Queue, drawn queue.Entry) error
	}{
		{"leaves", func(store *storage.Store, q queue.Queue, drawn queue.Entry) error {
			_, err := store.Leave(ctx, q.ID, token.Hash(drawn.CustomerName+q.ID))
			return err
		}},
		{"is skipped", func(store *storage.Store, q queue.Queue, drawn queue.Entry) error {
			_, err := store.SkipEntry(ctx, q.ID, drawn.ID, testOwner)
			return err
		}},
		{"is marked served from the list", func(store *storage.Store, q queue.Queue, drawn queue.Entry) error {
			_, err := store.AttendEntry(ctx, q.ID, drawn.ID, testOwner)
			return err
		}},
		{"is called by name", func(store *storage.Store, q queue.Queue, drawn queue.Entry) error {
			_, err := store.ServeEntry(ctx, q.ID, drawn.ID, "", testOwner)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			q := newDrawQueue(t, store, 10)

			for i := range 4 {
				join(t, store, q.ID, fmt.Sprintf("team-%d", i))
			}
			serveNext(t, store, q.ID)

			entries, err := store.ListActiveEntries(ctx, q.ID)
			if err != nil {
				t.Fatalf("list entries: %v", err)
			}
			drawn := entryByNumber(t, entries, upNext(t, store, q.ID))
			if drawn.DrawnAt == nil {
				t.Fatal("the drawn entry carries no drawnAt")
			}

			if err := tc.vacate(store, q, drawn); err != nil {
				t.Fatalf("vacate: %v", err)
			}

			replacement := upNext(t, store, q.ID)
			if replacement == 0 {
				t.Fatal("nobody drawn to replace them")
			}
			if replacement == drawn.Number {
				t.Fatalf("the replacement is the same number, %d", replacement)
			}
		})
	}
}

func TestCallingSomeoneElseByNameLeavesTheDrawAlone(t *testing.T) {
	store := newTestStore(t)
	q := newDrawQueue(t, store, 10)
	ctx := context.Background()

	for i := range 4 {
		join(t, store, q.ID, fmt.Sprintf("team-%d", i))
	}
	serveNext(t, store, q.ID)
	drawn := upNext(t, store, q.ID)

	entries, err := store.ListActiveEntries(ctx, q.ID)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	var other queue.Entry
	for _, entry := range entries {
		if entry.Status == queue.EntryWaiting && entry.Number != drawn {
			other = entry
			break
		}
	}

	if _, err := store.ServeEntry(ctx, q.ID, other.ID, "", testOwner); err != nil {
		t.Fatalf("serve by name: %v", err)
	}
	if got := upNext(t, store, q.ID); got != drawn {
		t.Fatalf("up next = %d after calling someone else, want it left at %d", got, drawn)
	}
}

func TestConcurrentDrawsCallEachNumberOnce(t *testing.T) {
	store := newTestStore(t)
	q := newDrawQueue(t, store, 10)

	for i := range 6 {
		join(t, store, q.ID, fmt.Sprintf("team-%d", i))
	}

	const operators = 5

	var wg sync.WaitGroup
	called := make([]int, operators)
	start := make(chan struct{})

	for i := range operators {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := store.ServeNext(context.Background(), q.ID, "", testOwner)
			if err != nil || result.Served == nil {
				return
			}
			called[i] = result.Served.Number
		}()
	}
	close(start)
	wg.Wait()

	seen := map[int]bool{}
	for _, number := range called {
		if number == 0 {
			continue
		}
		if seen[number] {
			t.Fatalf("number %d was called twice", number)
		}
		seen[number] = true
	}

	drawn := upNext(t, store, q.ID)
	if drawn == 0 {
		t.Fatal("nothing drawn with people still waiting")
	}
	if seen[drawn] {
		t.Fatalf("the drawn number %d has already been called", drawn)
	}
}

// A draw has a fixed number of places. Being called does not give a place
// back; cancelling does; a reset starts the count again.
func TestDrawCapacityCountsNumbersHandedOut(t *testing.T) {
	store := newTestStore(t)
	q := newDrawQueue(t, store, 3)
	ctx := context.Background()

	for i := range 3 {
		join(t, store, q.ID, fmt.Sprintf("team-%d", i))
	}
	serveNext(t, store, q.ID)
	serveNext(t, store, q.ID)

	if _, err := store.Join(ctx, q.ID, "late", token.Hash("late"+q.ID)); !errors.Is(err, queue.ErrQueueFull) {
		t.Fatalf("join after two were called = %v, want ErrQueueFull", err)
	}
	if state := publicState(t, store, q.ID); !state.IsFull || state.PlacesTaken != 3 {
		t.Fatalf("isFull = %v, placesTaken = %d; want full at 3", state.IsFull, state.PlacesTaken)
	}

	state := publicState(t, store, q.ID)
	if len(state.WaitingNumbers) != 1 {
		t.Fatalf("waiting = %v, want one left", state.WaitingNumbers)
	}
	waiting := state.WaitingNumbers[0]
	entries, err := store.ListActiveEntries(ctx, q.ID)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	leaver := entryByNumber(t, entries, waiting)
	if _, err := store.Leave(ctx, q.ID, token.Hash(leaver.CustomerName+q.ID)); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := store.Join(ctx, q.ID, "late", token.Hash("late"+q.ID)); err != nil {
		t.Fatalf("join after a cancel freed a place: %v", err)
	}

	if _, err := store.ResetQueue(ctx, q.ID); err != nil {
		t.Fatalf("reset: %v", err)
	}
	for i := range 3 {
		join(t, store, q.ID, fmt.Sprintf("next-event-%d", i))
	}
	if state := publicState(t, store, q.ID); state.PlacesTaken != 3 {
		t.Fatalf("placesTaken after reset and three joins = %d, want 3", state.PlacesTaken)
	}
}

func TestResetEmptiesTheDrawAndKeepsItsHistory(t *testing.T) {
	store := newTestStore(t)
	q := newDrawQueue(t, store, 10)
	ctx := context.Background()

	for i := range 3 {
		join(t, store, q.ID, fmt.Sprintf("team-%d", i))
	}
	serveNext(t, store, q.ID)
	drawn := upNext(t, store, q.ID)

	if _, err := store.ResetQueue(ctx, q.ID); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := upNext(t, store, q.ID); got != 0 {
		t.Fatalf("up next after reset = %d, want nothing", got)
	}

	history, err := store.History(ctx, q.ID, 50, "")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for _, row := range history {
		if row.Number == drawn && row.Status == queue.EntryCleared {
			if row.DrawnAt == nil {
				t.Fatal("the cleared entry lost when it was drawn")
			}
			return
		}
	}
	t.Fatalf("number %d is not in history as cleared", drawn)
}

func TestServingOrderSwitches(t *testing.T) {
	ctx := context.Background()
	random := queue.ServingRandom
	inOrder := queue.ServingInOrder

	t.Run("back to in order drops the draw", func(t *testing.T) {
		store := newTestStore(t)
		q := newDrawQueue(t, store, 10)
		for i := range 3 {
			join(t, store, q.ID, fmt.Sprintf("team-%d", i))
		}
		serveNext(t, store, q.ID)

		if _, err := store.UpdateQueue(ctx, q.ID, storage.UpdateQueueParams{ServingOrder: &inOrder}); err != nil {
			t.Fatalf("switch to in order: %v", err)
		}
		if got := upNext(t, store, q.ID); got != 0 {
			t.Fatalf("up next after switching back = %d, want nothing", got)
		}
		if state := publicState(t, store, q.ID); len(state.Estimates) == 0 {
			t.Fatal("estimates did not come back with in-order serving")
		}
	})

	t.Run("to a draw draws nothing yet", func(t *testing.T) {
		store := newTestStore(t)
		capacity := 10
		q := newTestQueue(t, store, &capacity)
		for i := range 3 {
			join(t, store, q.ID, fmt.Sprintf("team-%d", i))
		}

		if _, err := store.UpdateQueue(ctx, q.ID, storage.UpdateQueueParams{ServingOrder: &random}); err != nil {
			t.Fatalf("switch to a draw: %v", err)
		}
		if got := upNext(t, store, q.ID); got != 0 {
			t.Fatalf("up next straight after switching = %d, want nothing until the first call", got)
		}
	})

	t.Run("to a draw with no capacity is refused", func(t *testing.T) {
		store := newTestStore(t)
		q := newTestQueue(t, store, nil)

		_, err := store.UpdateQueue(ctx, q.ID, storage.UpdateQueueParams{ServingOrder: &random})
		if !errors.Is(err, queue.ErrInvalidInput) {
			t.Fatalf("draw with no capacity = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("clearing the capacity of a draw is refused", func(t *testing.T) {
		store := newTestStore(t)
		q := newDrawQueue(t, store, 10)

		_, err := store.UpdateQueue(ctx, q.ID, storage.UpdateQueueParams{MaxCapacitySet: true})
		if !errors.Is(err, queue.ErrInvalidInput) {
			t.Fatalf("clearing a draw's capacity = %v, want ErrInvalidInput", err)
		}
	})
}

func TestNounsDefaultAndUpdate(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	if q.PersonNoun != "customer" || q.PeopleNoun != "customers" {
		t.Fatalf("nouns = %q/%q, want customer/customers", q.PersonNoun, q.PeopleNoun)
	}

	person, people := "guest", "guests"
	updated, err := store.UpdateQueue(ctx, q.ID, storage.UpdateQueueParams{PersonNoun: &person, PeopleNoun: &people})
	if err != nil {
		t.Fatalf("update nouns: %v", err)
	}
	if updated.PersonNoun != "guest" || updated.PeopleNoun != "guests" {
		t.Fatalf("nouns = %q/%q, want guest/guests", updated.PersonNoun, updated.PeopleNoun)
	}
	if state := publicState(t, store, q.ID); state.Queue.PeopleNoun != "guests" {
		t.Fatalf("public summary noun = %q, want guests", state.Queue.PeopleNoun)
	}
}
