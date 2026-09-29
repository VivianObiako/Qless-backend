package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
)

func newRandomNumbersQueue(t *testing.T, store *storage.Store, places int) queue.Queue {
	t.Helper()

	q, err := store.CreateQueue(context.Background(), storage.CreateQueueParams{
		Name:                     t.Name(),
		AverageServiceMinutes:    10,
		MaxCapacity:              &places,
		Numbering:                queue.NumberRandom,
		NewOwnerRecoveryCodeHash: token.Hash("recovery-" + t.Name() + "-" + freshSecret(t)),
		NewOwnerTokenHash:        token.Hash("owner-" + t.Name() + "-" + freshSecret(t)),
	})
	if err != nil {
		t.Fatalf("create random-numbers queue: %v", err)
	}
	return q.Queue
}

// Forty people joining at once each get a different number, all within 1 to
// the places, and the forty-first is turned away.
func TestRandomNumbersNeverRepeatAndStayInRange(t *testing.T) {
	store := newTestStore(t)
	q := newRandomNumbersQueue(t, store, 40)
	ctx := context.Background()

	var wg sync.WaitGroup
	numbers := make([]int, 40)
	errs := make([]error, 40)
	start := make(chan struct{})
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			entry, err := store.Join(ctx, q.ID, fmt.Sprintf("team-%d", i), token.Hash(fmt.Sprintf("team-%d%s", i, q.ID)))
			numbers[i], errs[i] = entry.Number, err
		}()
	}
	close(start)
	wg.Wait()

	seen := map[int]bool{}
	for i, n := range numbers {
		if errs[i] != nil {
			t.Fatalf("join %d: %v", i, errs[i])
		}
		if n < 1 || n > 40 {
			t.Fatalf("number %d is outside 1 to 40", n)
		}
		if seen[n] {
			t.Fatalf("number %d was handed out twice", n)
		}
		seen[n] = true
	}

	if _, err := store.Join(ctx, q.ID, "late", token.Hash("late"+q.ID)); !errors.Is(err, queue.ErrQueueFull) {
		t.Fatalf("the forty-first join = %v, want ErrQueueFull", err)
	}
}

// The point of it: the first to scan does not get number 1. Twelve numbers
// coming out in joining order by chance is one in 479 million.
func TestRandomNumbersAreNotJoiningOrder(t *testing.T) {
	store := newTestStore(t)
	q := newRandomNumbersQueue(t, store, 12)

	var got []int
	for i := range 12 {
		got = append(got, join(t, store, q.ID, fmt.Sprintf("team-%d", i)).Number)
	}
	if sort.IntsAreSorted(got) {
		t.Fatalf("twelve numbers came out in joining order: %v", got)
	}
}

// Numbers are random; the calling is not. The lowest number goes first.
func TestRandomNumbersAreCalledLowestFirst(t *testing.T) {
	store := newTestStore(t)
	q := newRandomNumbersQueue(t, store, 20)

	lowest := 21
	for i := range 6 {
		if n := join(t, store, q.ID, fmt.Sprintf("team-%d", i)).Number; n < lowest {
			lowest = n
		}
	}
	if called := serveNext(t, store, q.ID); called != lowest {
		t.Fatalf("called %d first, want the lowest number %d", called, lowest)
	}
}

// A number once called keeps its place, so nobody else can be handed it. A
// cancelled number goes back into the pool.
func TestRandomNumberPlaces(t *testing.T) {
	store := newTestStore(t)
	q := newRandomNumbersQueue(t, store, 3)
	ctx := context.Background()

	entries := []queue.Entry{
		join(t, store, q.ID, "a"),
		join(t, store, q.ID, "b"),
		join(t, store, q.ID, "c"),
	}
	serveNext(t, store, q.ID)
	if _, err := store.Join(ctx, q.ID, "late", token.Hash("late"+q.ID)); !errors.Is(err, queue.ErrQueueFull) {
		t.Fatalf("join after a call = %v, want ErrQueueFull: a called number keeps its place", err)
	}

	state := publicState(t, store, q.ID)
	leaving := entries[0]
	for _, e := range entries {
		if e.Number == state.WaitingNumbers[0] {
			leaving = e
		}
	}
	if _, err := store.Leave(ctx, q.ID, token.Hash(leaving.CustomerName+q.ID)); err != nil {
		t.Fatalf("leave: %v", err)
	}
	late, err := store.Join(ctx, q.ID, "late", token.Hash("late"+q.ID))
	if err != nil {
		t.Fatalf("join after a cancel: %v", err)
	}
	if late.Number != leaving.Number {
		t.Fatalf("the late team got %d, want the freed number %d, the only one left", late.Number, leaving.Number)
	}
}

// Switching mode never hands out a number somebody holds.
func TestSwitchingNumberingNeverRepeatsANumber(t *testing.T) {
	ctx := context.Background()
	sequential, random := queue.NumberSequential, queue.NumberRandom

	t.Run("back to sequential", func(t *testing.T) {
		store := newTestStore(t)
		q := newRandomNumbersQueue(t, store, 30)
		highest := 0
		for i := range 5 {
			if n := join(t, store, q.ID, fmt.Sprintf("team-%d", i)).Number; n > highest {
				highest = n
			}
		}
		if _, err := store.UpdateQueue(ctx, q.ID, storage.UpdateQueueParams{Numbering: &sequential}); err != nil {
			t.Fatalf("switch to sequential: %v", err)
		}
		if n := join(t, store, q.ID, "after").Number; n != highest+1 {
			t.Fatalf("the next number is %d, want %d, one above the highest taken", n, highest+1)
		}
	})

	t.Run("into random", func(t *testing.T) {
		store := newTestStore(t)
		places := 5
		q := newTestQueue(t, store, &places)
		for i := range 3 {
			join(t, store, q.ID, fmt.Sprintf("early-%d", i))
		}
		if _, err := store.UpdateQueue(ctx, q.ID, storage.UpdateQueueParams{Numbering: &random}); err != nil {
			t.Fatalf("switch to random numbers: %v", err)
		}
		got := []int{join(t, store, q.ID, "x").Number, join(t, store, q.ID, "y").Number}
		sort.Ints(got)
		if got[0] != 4 || got[1] != 5 {
			t.Fatalf("after 1 to 3 were taken, the random numbers were %v, want 4 and 5", got)
		}
	})
}

func TestRandomNumbersSettingsGuards(t *testing.T) {
	ctx := context.Background()
	random := queue.NumberRandom
	randomCall := queue.ServingRandom

	store := newTestStore(t)
	noPlaces := newTestQueue(t, store, nil)
	if _, err := store.UpdateQueue(ctx, noPlaces.ID, storage.UpdateQueueParams{Numbering: &random}); !errors.Is(err, queue.ErrInvalidInput) {
		t.Errorf("random numbers with no places = %v, want ErrInvalidInput", err)
	}

	draw := newDrawQueue(t, store, 10)
	if _, err := store.UpdateQueue(ctx, draw.ID, storage.UpdateQueueParams{Numbering: &random}); !errors.Is(err, queue.ErrInvalidInput) {
		t.Errorf("random numbers on a draw = %v, want ErrInvalidInput", err)
	}

	numbers := newRandomNumbersQueue(t, store, 10)
	if _, err := store.UpdateQueue(ctx, numbers.ID, storage.UpdateQueueParams{ServingOrder: &randomCall}); !errors.Is(err, queue.ErrInvalidInput) {
		t.Errorf("a draw on random numbers = %v, want ErrInvalidInput", err)
	}
}
