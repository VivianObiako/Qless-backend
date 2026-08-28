package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/vivianobiako/qless/api/internal/config"
	"github.com/vivianobiako/qless/api/internal/database"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
)

// Each test creates its own queue, so tests share the database without
// coordinating on truncation and can run in any order.
func newTestStore(t *testing.T) *storage.Store {
	t.Helper()

	url := config.TestDatabaseURL()
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; run `make up` and copy .env.example to .env")
	}
	if err := database.Migrate(url); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	store, err := storage.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// The owner minted for each queue has to be unique across runs: the test
// database is not truncated between them, and both owner hashes are unique
// columns.
func freshSecret(t *testing.T) string {
	t.Helper()

	raw, err := token.New()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	return raw
}

// Storage tests act as the owner: acted_by_operator_id stays null, which is
// what the acted_by_matches_type constraint expects for an owner.
var testOwner = queue.Actor{Type: queue.PrincipalOwner}

func newTestQueue(t *testing.T, store *storage.Store, capacity *int) queue.Queue {
	t.Helper()

	q, err := store.CreateQueue(context.Background(), storage.CreateQueueParams{
		Name:                     t.Name(),
		AverageServiceMinutes:    15,
		MaxCapacity:              capacity,
		NewOwnerRecoveryCodeHash: token.Hash("recovery-" + t.Name() + "-" + freshSecret(t)),
		NewOwnerTokenHash:        token.Hash("owner-" + t.Name() + "-" + freshSecret(t)),
	})
	if err != nil {
		t.Fatalf("create queue: %v", err)
	}
	return q.Queue
}

func join(t *testing.T, store *storage.Store, queueID, name string) queue.Entry {
	t.Helper()

	entry, err := store.Join(context.Background(), queueID, name, token.Hash(name+queueID))
	if err != nil {
		t.Fatalf("join as %s: %v", name, err)
	}
	return entry
}

func TestJoinAssignsSequentialNumbers(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)

	for want := 1; want <= 3; want++ {
		entry := join(t, store, q.ID, fmt.Sprintf("customer-%d", want))
		if entry.Number != want {
			t.Errorf("entry %d: got number %d, want %d", want, entry.Number, want)
		}
		if entry.Status != queue.EntryWaiting {
			t.Errorf("new entry status = %s, want WAITING", entry.Status)
		}
	}
}

// The requirement that matters most: two customers tapping Join at the same
// instant must never receive the same number.
func TestConcurrentJoinsGetUniqueNumbers(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)

	const joiners = 50

	var wg sync.WaitGroup
	numbers := make([]int, joiners)
	errs := make([]error, joiners)

	start := make(chan struct{})
	for i := range joiners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release everyone at once to maximise contention
			entry, err := store.Join(
				context.Background(),
				q.ID,
				fmt.Sprintf("racer-%d", i),
				token.Hash(fmt.Sprintf("racer-token-%d-%s", i, q.ID)),
			)
			numbers[i] = entry.Number
			errs[i] = err
		}()
	}
	close(start)
	wg.Wait()

	seen := make(map[int]int, joiners)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("joiner %d failed: %v", i, err)
		}
		if previous, duplicate := seen[numbers[i]]; duplicate {
			t.Fatalf("number %d assigned to both joiner %d and joiner %d", numbers[i], previous, i)
		}
		seen[numbers[i]] = i
	}

	for want := 1; want <= joiners; want++ {
		if _, ok := seen[want]; !ok {
			t.Errorf("number %d was never assigned; numbers must be consecutive", want)
		}
	}
}

func TestDuplicateJoinReturnsExistingEntry(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	customerToken := token.Hash("returning-customer")

	first, err := store.Join(ctx, q.ID, "Vivian", customerToken)
	if err != nil {
		t.Fatalf("first join: %v", err)
	}

	second, err := store.Join(ctx, q.ID, "Vivian Typed Again", customerToken)
	if !errors.Is(err, queue.ErrAlreadyJoined) {
		t.Fatalf("second join error = %v, want ErrAlreadyJoined", err)
	}
	if second.ID != first.ID {
		t.Errorf("second join returned entry %s, want the original %s", second.ID, first.ID)
	}
	if second.Number != first.Number {
		t.Errorf("second join returned number %d, want %d", second.Number, first.Number)
	}
	if second.CustomerName != "Vivian" {
		t.Errorf("customer name changed to %q; a rejoin must not overwrite the original", second.CustomerName)
	}
}

// A customer who leaves is not locked out: the uniqueness index covers active
// entries only, so they can rejoin and take a fresh number.
func TestRejoinAfterLeavingGetsNewNumber(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	customerToken := token.Hash("leaver")

	first, err := store.Join(ctx, q.ID, "Sarah", customerToken)
	if err != nil {
		t.Fatalf("join: %v", err)
	}

	left, err := store.Leave(ctx, q.ID, customerToken)
	if err != nil {
		t.Fatalf("leave: %v", err)
	}
	if left.Status != queue.EntryLeft {
		t.Errorf("status after leaving = %s, want LEFT", left.Status)
	}

	second, err := store.Join(ctx, q.ID, "Sarah", customerToken)
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if second.Number <= first.Number {
		t.Errorf("rejoin number %d should be higher than the original %d", second.Number, first.Number)
	}
}

func TestLeaveWithoutAnActiveEntry(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)

	_, err := store.Leave(context.Background(), q.ID, token.Hash("never-joined"))
	if !errors.Is(err, queue.ErrNotInQueue) {
		t.Fatalf("leave error = %v, want ErrNotInQueue", err)
	}
}

func TestCapacityBlocksJoinAndReopensAfterLeave(t *testing.T) {
	store := newTestStore(t)
	capacity := 2
	q := newTestQueue(t, store, &capacity)
	ctx := context.Background()

	join(t, store, q.ID, "first")
	second := token.Hash("second-" + q.ID)
	if _, err := store.Join(ctx, q.ID, "second", second); err != nil {
		t.Fatalf("second join: %v", err)
	}

	_, err := store.Join(ctx, q.ID, "third", token.Hash("third-"+q.ID))
	if !errors.Is(err, queue.ErrQueueFull) {
		t.Fatalf("third join error = %v, want ErrQueueFull", err)
	}

	if _, err := store.Leave(ctx, q.ID, second); err != nil {
		t.Fatalf("leave: %v", err)
	}

	if _, err := store.Join(ctx, q.ID, "third", token.Hash("third-"+q.ID)); err != nil {
		t.Fatalf("join after a departure freed a slot: %v", err)
	}
}

func TestJoinBlockedWhenPausedOrClosed(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		status  queue.Status
		wantErr error
	}{
		{queue.StatusPaused, queue.ErrQueuePaused},
		{queue.StatusClosed, queue.ErrQueueClosed},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			store := newTestStore(t)
			q := newTestQueue(t, store, nil)

			if _, err := store.SetStatus(ctx, q.ID, tc.status); err != nil {
				t.Fatalf("set status: %v", err)
			}

			_, err := store.Join(ctx, q.ID, "hopeful", token.Hash("hopeful-"+q.ID))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("join error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// Customers already in a paused queue keep their place and can still read it.
func TestPausingPreservesExistingEntries(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	customerToken := token.Hash("waiting-through-pause")
	if _, err := store.Join(ctx, q.ID, "Patient", customerToken); err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := store.SetStatus(ctx, q.ID, queue.StatusPaused); err != nil {
		t.Fatalf("pause: %v", err)
	}

	entry, err := store.MyEntry(ctx, q.ID, customerToken)
	if err != nil {
		t.Fatalf("read entry while paused: %v", err)
	}
	if entry.Status != queue.EntryWaiting {
		t.Errorf("status while paused = %s, want WAITING", entry.Status)
	}
}

func TestServeNextPromotesLowestNumberAndAttendsPrevious(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	first := join(t, store, q.ID, "first")
	second := join(t, store, q.ID, "second")

	result, err := store.ServeNext(ctx, q.ID, testOwner)
	if err != nil {
		t.Fatalf("first serve next: %v", err)
	}
	if result.Attended != nil {
		t.Errorf("nothing was being served, so nothing should have been attended, got %v", result.Attended)
	}
	if result.Served == nil || result.Served.Number != first.Number {
		t.Fatalf("served %v, want number %d", result.Served, first.Number)
	}

	result, err = store.ServeNext(ctx, q.ID, testOwner)
	if err != nil {
		t.Fatalf("second serve next: %v", err)
	}
	if result.Attended == nil || result.Attended.Number != first.Number {
		t.Fatalf("attended %v, want number %d", result.Attended, first.Number)
	}
	if result.Attended.CompletedAt == nil {
		t.Error("attended entry has no completed_at; service duration would be unrecoverable")
	}
	if result.Served == nil || result.Served.Number != second.Number {
		t.Fatalf("served %v, want number %d", result.Served, second.Number)
	}
	if result.Served.StartedAt == nil {
		t.Error("served entry has no started_at")
	}
}

func TestServeNextOnEmptyQueueAttendsCurrentAndStops(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	only := join(t, store, q.ID, "only")

	if _, err := store.ServeNext(ctx, q.ID, testOwner); err != nil {
		t.Fatalf("serve next: %v", err)
	}

	result, err := store.ServeNext(ctx, q.ID, testOwner)
	if err != nil {
		t.Fatalf("serve next on empty queue: %v", err)
	}
	if result.Served != nil {
		t.Errorf("served %v from an empty queue", result.Served)
	}
	if result.Attended == nil || result.Attended.Number != only.Number {
		t.Fatalf("attended %v, want number %d", result.Attended, only.Number)
	}
}

// Two operators clicking Serve Next at the same moment must not both promote a
// customer. The partial unique index is the backstop.
func TestConcurrentServeNextServesEachCustomerOnce(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)

	for i := range 5 {
		join(t, store, q.ID, fmt.Sprintf("customer-%d", i))
	}

	const operators = 5

	var wg sync.WaitGroup
	served := make([]int, operators)
	start := make(chan struct{})

	for i := range operators {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := store.ServeNext(context.Background(), q.ID, testOwner)
			if err != nil {
				return
			}
			if result.Served != nil {
				served[i] = result.Served.Number
			}
		}()
	}
	close(start)
	wg.Wait()

	seen := map[int]bool{}
	for _, number := range served {
		if number == 0 {
			continue
		}
		if seen[number] {
			t.Fatalf("customer %d was served by two operators", number)
		}
		seen[number] = true
	}
}

func TestPublicStateExposesNumbersOnly(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	join(t, store, q.ID, "Vivian")
	join(t, store, q.ID, "John")
	if _, err := store.ServeNext(ctx, q.ID, testOwner); err != nil {
		t.Fatalf("serve next: %v", err)
	}

	state, err := store.PublicState(ctx, q)
	if err != nil {
		t.Fatalf("public state: %v", err)
	}

	if state.ServingNumber == nil || *state.ServingNumber != 1 {
		t.Errorf("serving number = %v, want 1", state.ServingNumber)
	}
	if len(state.WaitingNumbers) != 1 || state.WaitingNumbers[0] != 2 {
		t.Errorf("waiting numbers = %v, want [2]", state.WaitingNumbers)
	}
	if state.WaitingCount != 1 {
		t.Errorf("waiting count = %d, want 1", state.WaitingCount)
	}
	if got := state.PeopleAhead(2); got != 0 {
		t.Errorf("people ahead of #2 = %d, want 0", got)
	}
}
