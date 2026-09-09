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

			if _, err := store.SetStatus(ctx, q.ID, tc.status, ""); err != nil {
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
	if _, err := store.SetStatus(ctx, q.ID, queue.StatusPaused, ""); err != nil {
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

	result, err := store.ServeNext(ctx, q.ID, "", testOwner)
	if err != nil {
		t.Fatalf("first serve next: %v", err)
	}
	if result.Attended != nil {
		t.Errorf("nothing was being served, so nothing should have been attended, got %v", result.Attended)
	}
	if result.Served == nil || result.Served.Number != first.Number {
		t.Fatalf("served %v, want number %d", result.Served, first.Number)
	}

	result, err = store.ServeNext(ctx, q.ID, "", testOwner)
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

	if _, err := store.ServeNext(ctx, q.ID, "", testOwner); err != nil {
		t.Fatalf("serve next: %v", err)
	}

	result, err := store.ServeNext(ctx, q.ID, "", testOwner)
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
			result, err := store.ServeNext(context.Background(), q.ID, "", testOwner)
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
	if _, err := store.ServeNext(ctx, q.ID, "", testOwner); err != nil {
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

// With two seats, calls fill the free seats lowest first and never touch the
// other seat's person. Standing down happens only when a seat is reused.
func TestServeNextFillsFreeSeatsLowestFirst(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	seats, err := store.Seats(ctx, q.ID)
	if err != nil {
		t.Fatalf("list seats: %v", err)
	}
	counter := seats[0]
	chair2, err := store.CreateSeat(ctx, q.ID, "Chair 2")
	if err != nil {
		t.Fatalf("create seat: %v", err)
	}

	join(t, store, q.ID, "first")
	join(t, store, q.ID, "second")
	join(t, store, q.ID, "third")

	first, err := store.ServeNext(ctx, q.ID, "", testOwner)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first.Served == nil || first.Served.SeatID == nil || *first.Served.SeatID != counter.ID {
		t.Fatalf("first call landed on %+v, want the counter", first.Served)
	}

	second, err := store.ServeNext(ctx, q.ID, "", testOwner)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second.Attended != nil {
		t.Fatalf("second call stood down %+v; the counter's person must be left alone", second.Attended)
	}
	if second.Served == nil || second.Served.SeatID == nil || *second.Served.SeatID != chair2.ID {
		t.Fatalf("second call landed on %+v, want Chair 2", second.Served)
	}

	// Every seat taken and none named: the caller has to say.
	if _, err := store.ServeNext(ctx, q.ID, "", testOwner); !errors.Is(err, queue.ErrNoFreeSeat) {
		t.Fatalf("call with every seat taken = %v, want ErrNoFreeSeat", err)
	}

	// Naming the counter reuses it: its person is stood down, Chair 2's is not.
	third, err := store.ServeNext(ctx, q.ID, counter.ID, testOwner)
	if err != nil {
		t.Fatalf("call to the counter: %v", err)
	}
	if third.Attended == nil || third.Attended.Number != 1 || third.Attended.Status != queue.EntrySkipped {
		t.Fatalf("stood down %+v, want #1 skipped (never served)", third.Attended)
	}
	if third.Served == nil || third.Served.Number != 3 || *third.Served.SeatID != counter.ID {
		t.Fatalf("called %+v, want #3 on the counter", third.Served)
	}

	active, err := store.ListActiveEntries(ctx, q.ID)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 2 || active[0].Number != 3 || active[1].Number != 2 {
		t.Fatalf("active = %+v, want #3 on the counter then #2 on Chair 2", active)
	}

	// A closed seat and a seat from nowhere are refused.
	if _, err := store.Pool().Exec(ctx, `UPDATE seats SET active = false WHERE id = $1`, chair2.ID); err != nil {
		t.Fatalf("close seat: %v", err)
	}
	if _, err := store.ServeNext(ctx, q.ID, chair2.ID, testOwner); !errors.Is(err, queue.ErrSeatClosed) {
		t.Fatalf("call to a closed seat = %v, want ErrSeatClosed", err)
	}
	other := newTestQueue(t, store, nil)
	otherSeats, _ := store.Seats(ctx, other.ID)
	if _, err := store.ServeNext(ctx, q.ID, otherSeats[0].ID, testOwner); !errors.Is(err, queue.ErrSeatNotFound) {
		t.Fatalf("call to another queue's seat = %v, want ErrSeatNotFound", err)
	}
}

// A skipped person is recalled to a seat like anybody else, which may be a
// different one from where they were first called.
func TestRecallLandsOnTheSeatNamed(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	chair2, err := store.CreateSeat(ctx, q.ID, "Chair 2")
	if err != nil {
		t.Fatalf("create seat: %v", err)
	}
	first := join(t, store, q.ID, "first")
	join(t, store, q.ID, "second")

	if _, err := store.ServeNext(ctx, q.ID, "", testOwner); err != nil {
		t.Fatalf("call: %v", err)
	}
	if _, err := store.SkipEntry(ctx, q.ID, first.ID, testOwner); err != nil {
		t.Fatalf("skip: %v", err)
	}

	recalled, err := store.ServeEntry(ctx, q.ID, first.ID, chair2.ID, testOwner)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if recalled.Served == nil || recalled.Served.SeatID == nil || *recalled.Served.SeatID != chair2.ID {
		t.Fatalf("recalled to %+v, want Chair 2", recalled.Served)
	}
	if recalled.Served.ServedAt == nil {
		t.Error("a recalled person is there already; service should have begun")
	}
}

// The public state says every number being served and where, with the most
// recent call kept as servingNumber for boards from before seats. Closed
// seats are listed but do not count as open.
func TestPublicStateListsEverySeat(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	chair2, err := store.CreateSeat(ctx, q.ID, "Chair 2")
	if err != nil {
		t.Fatalf("create seat: %v", err)
	}
	if _, err := store.CreateSeat(ctx, q.ID, "Chair 3"); err != nil {
		t.Fatalf("create seat: %v", err)
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE seats SET active = false WHERE queue_id = $1 AND name = 'Chair 3'`, q.ID); err != nil {
		t.Fatalf("close seat: %v", err)
	}

	for i := 1; i <= 5; i++ {
		join(t, store, q.ID, fmt.Sprintf("c%d", i))
	}
	if _, err := store.ServeNext(ctx, q.ID, "", testOwner); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := store.ServeNext(ctx, q.ID, chair2.ID, testOwner); err != nil {
		t.Fatalf("second call: %v", err)
	}

	state, err := store.PublicState(ctx, q)
	if err != nil {
		t.Fatalf("public state: %v", err)
	}
	if len(state.Seats) != 3 || state.OpenSeats != 2 {
		t.Fatalf("seats = %+v open %d, want three listed and two open", state.Seats, state.OpenSeats)
	}
	if len(state.Serving) != 2 || state.Serving[0].Number != 1 || state.Serving[0].SeatName != "Counter" ||
		state.Serving[1].Number != 2 || state.Serving[1].SeatName != "Chair 2" {
		t.Fatalf("serving = %+v, want #1 on the counter and #2 on Chair 2", state.Serving)
	}
	if state.ServingNumber == nil || *state.ServingNumber != 2 {
		t.Fatalf("servingNumber = %v, want the most recent call, 2", state.ServingNumber)
	}
	if state.WaitingCount != 3 || len(state.Estimates) != 4 {
		t.Fatalf("waiting %d with %d estimates, want 3 and 4", state.WaitingCount, len(state.Estimates))
	}
	// Two open seats: one and two ahead are one turn, three ahead is two.
	if state.Estimates[1].LowMinutes != state.Estimates[2].LowMinutes || state.Estimates[3].LowMinutes <= state.Estimates[2].LowMinutes {
		t.Fatalf("estimates %v do not divide by two open seats", state.Estimates)
	}
}

// Seats are managed as rows: named, ordered, opened and closed, and removed
// softly. The rules that protect a customer — no closing or removing a chair
// somebody is on, never fewer than one chair — are the store's.
func TestSeatSettings(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	counter, err := store.Seats(ctx, q.ID)
	if err != nil {
		t.Fatalf("list seats: %v", err)
	}
	if _, err := store.RemoveSeat(ctx, q.ID, counter[0].ID); !errors.Is(err, queue.ErrLastSeat) {
		t.Fatalf("removing the only seat = %v, want ErrLastSeat", err)
	}

	chair2, err := store.CreateSeat(ctx, q.ID, "Chair 2")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.CreateSeat(ctx, q.ID, "chair 2"); !errors.Is(err, queue.ErrInvalidInput) {
		t.Fatalf("duplicate name = %v, want invalid input", err)
	}
	chair3, err := store.CreateSeat(ctx, q.ID, "Chair 3")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Move Chair 3 to the front; the rest shift down and stay 1..n.
	first := 1
	seats, err := store.UpdateSeat(ctx, q.ID, chair3.ID, storage.UpdateSeatParams{Position: &first})
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if names(seats) != "Chair 3,Counter,Chair 2" || seats[0].Position != 1 || seats[2].Position != 3 {
		t.Fatalf("order after move = %v, want Chair 3 first", names(seats))
	}
	last := 3
	seats, err = store.UpdateSeat(ctx, q.ID, chair3.ID, storage.UpdateSeatParams{Position: &last})
	if err != nil {
		t.Fatalf("reorder back: %v", err)
	}
	if names(seats) != "Counter,Chair 2,Chair 3" {
		t.Fatalf("order after moving back = %v", names(seats))
	}

	// A chair with somebody on it cannot be closed or removed.
	join(t, store, q.ID, "a")
	if _, err := store.ServeNext(ctx, q.ID, chair2.ID, testOwner); err != nil {
		t.Fatalf("call: %v", err)
	}
	closed := false
	if _, err := store.UpdateSeat(ctx, q.ID, chair2.ID, storage.UpdateSeatParams{Active: &closed}); !errors.Is(err, queue.ErrSeatOccupied) {
		t.Fatalf("closing an occupied seat = %v, want ErrSeatOccupied", err)
	}
	if _, err := store.RemoveSeat(ctx, q.ID, chair2.ID); !errors.Is(err, queue.ErrSeatOccupied) {
		t.Fatalf("removing an occupied seat = %v, want ErrSeatOccupied", err)
	}

	// An empty one can be closed, renamed and removed, and stays resolvable.
	seats, err = store.UpdateSeat(ctx, q.ID, chair3.ID, storage.UpdateSeatParams{Active: &closed})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if seats[2].Active {
		t.Fatal("Chair 3 should be closed")
	}
	renamed := "Room 3"
	if _, err := store.UpdateSeat(ctx, q.ID, chair3.ID, storage.UpdateSeatParams{Name: &renamed}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	seats, err = store.RemoveSeat(ctx, q.ID, chair3.ID)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if names(seats) != "Counter,Chair 2" {
		t.Fatalf("after removal = %v", names(seats))
	}
	if _, err := store.UpdateSeat(ctx, q.ID, chair3.ID, storage.UpdateSeatParams{Name: &renamed}); !errors.Is(err, queue.ErrSeatNotFound) {
		t.Fatalf("editing a removed seat = %v, want ErrSeatNotFound", err)
	}
	var stillNamed string
	if err := store.Pool().QueryRow(ctx, `SELECT name FROM seats WHERE id = $1`, chair3.ID).Scan(&stillNamed); err != nil || stillNamed != "Room 3" {
		t.Fatalf("removed seat name = %q (%v), want kept for history", stillNamed, err)
	}
}

func names(seats []queue.Seat) string {
	out := ""
	for i, seat := range seats {
		if i > 0 {
			out += ","
		}
		out += seat.Name
	}
	return out
}

// Who works a chair. The owner may take any chair and bump whoever is on
// it; an operator only a free one, and not at all where chairs are fixed.
// Everyone holds one chair per queue.
func TestSeatWorkers(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	var ownerID string
	if err := store.Pool().QueryRow(ctx, `SELECT owner_id FROM queues WHERE id = $1`, q.ID).Scan(&ownerID); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	owner := queue.Actor{Type: queue.PrincipalOwner, ID: ownerID, OwnerID: ownerID}

	ada, err := store.CreateOperator(ctx, storage.CreateOperatorParams{
		OwnerID: ownerID, DisplayName: "Ada", AccessCodeHash: token.Hash("ada-" + freshSecret(t)), QueueIDs: []string{q.ID},
	})
	if err != nil {
		t.Fatalf("hire Ada: %v", err)
	}
	adaActor := queue.Actor{Type: queue.PrincipalOperator, ID: ada.ID, OwnerID: ownerID}
	bola, err := store.CreateOperator(ctx, storage.CreateOperatorParams{
		OwnerID: ownerID, DisplayName: "Bola", AccessCodeHash: token.Hash("bola-" + freshSecret(t)),
	})
	if err != nil {
		t.Fatalf("hire Bola: %v", err)
	}

	seats, _ := store.Seats(ctx, q.ID)
	counter := seats[0]
	chair2, err := store.CreateSeat(ctx, q.ID, "Chair 2")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Assigning from the roster: the operator must work this queue.
	if _, err := store.AssignSeat(ctx, q.ID, counter.ID, &queue.SeatWorker{Type: queue.PrincipalOperator, OperatorID: bola.ID}); !errors.Is(err, queue.ErrOperatorNotFound) {
		t.Fatalf("assigning an operator not on the queue = %v, want ErrOperatorNotFound", err)
	}
	seats, err = store.AssignSeat(ctx, q.ID, counter.ID, &queue.SeatWorker{Type: queue.PrincipalOperator, OperatorID: ada.ID})
	if err != nil {
		t.Fatalf("assign Ada: %v", err)
	}
	if seats[0].Worker == nil || seats[0].Worker.Name != "Ada" || seats[0].Worker.OperatorID != ada.ID {
		t.Fatalf("counter worker = %+v, want Ada", seats[0].Worker)
	}

	// One chair per operator per queue: moving Ada frees the counter.
	seats, err = store.AssignSeat(ctx, q.ID, chair2.ID, &queue.SeatWorker{Type: queue.PrincipalOperator, OperatorID: ada.ID})
	if err != nil {
		t.Fatalf("move Ada: %v", err)
	}
	if seats[0].Worker != nil || seats[1].Worker == nil {
		t.Fatalf("after moving Ada: counter %+v, chair 2 %+v", seats[0].Worker, seats[1].Worker)
	}

	// An operator may not take a chair that is somebody's; the owner may.
	if _, err := store.TakeSeat(ctx, q.ID, chair2.ID, queue.Actor{Type: queue.PrincipalOperator, ID: bola.ID, OwnerID: ownerID}); !errors.Is(err, queue.ErrSeatTaken) {
		t.Fatalf("Bola taking Ada's chair = %v, want ErrSeatTaken", err)
	}
	seats, err = store.TakeSeat(ctx, q.ID, chair2.ID, owner)
	if err != nil {
		t.Fatalf("owner taking Ada's chair: %v", err)
	}
	if seats[1].Worker == nil || seats[1].Worker.Type != queue.PrincipalOwner {
		t.Fatalf("chair 2 after the owner took it = %+v", seats[1].Worker)
	}

	// Ada, bumped, takes the free counter; leaving makes it nobody's.
	seats, err = store.TakeSeat(ctx, q.ID, counter.ID, adaActor)
	if err != nil {
		t.Fatalf("Ada taking the counter: %v", err)
	}
	if seats[0].Worker == nil || seats[0].Worker.OperatorID != ada.ID {
		t.Fatalf("counter after Ada took it = %+v", seats[0].Worker)
	}
	seats, err = store.LeaveSeat(ctx, q.ID, counter.ID, adaActor)
	if err != nil {
		t.Fatalf("Ada leaving: %v", err)
	}
	if seats[0].Worker != nil {
		t.Fatalf("counter after Ada left = %+v, want nobody", seats[0].Worker)
	}

	// A chair with somebody being served at it is not free either.
	join(t, store, q.ID, "a")
	if _, err := store.ServeNext(ctx, q.ID, counter.ID, testOwner); err != nil {
		t.Fatalf("call: %v", err)
	}
	if _, err := store.TakeSeat(ctx, q.ID, counter.ID, adaActor); !errors.Is(err, queue.ErrSeatTaken) {
		t.Fatalf("taking a chair somebody is on = %v, want ErrSeatTaken", err)
	}

	// Fixed chairs: staff cannot pick or leave; the owner still assigns.
	fixed := true
	if _, err := store.UpdateQueue(ctx, q.ID, storage.UpdateQueueParams{SeatsFixed: &fixed}); err != nil {
		t.Fatalf("fix seats: %v", err)
	}
	if _, err := store.TakeSeat(ctx, q.ID, chair2.ID, adaActor); !errors.Is(err, queue.ErrSeatsFixed) {
		t.Fatalf("taking with fixed seats = %v, want ErrSeatsFixed", err)
	}
	seats, err = store.AssignSeat(ctx, q.ID, chair2.ID, &queue.SeatWorker{Type: queue.PrincipalOperator, OperatorID: ada.ID})
	if err != nil {
		t.Fatalf("assign with fixed seats: %v", err)
	}
	if _, err := store.LeaveSeat(ctx, q.ID, chair2.ID, adaActor); !errors.Is(err, queue.ErrSeatsFixed) {
		t.Fatalf("leaving with fixed seats = %v, want ErrSeatsFixed", err)
	}

	// Taking Ada off the queue, or revoking her, gives the chair up.
	none := []string{}
	if _, err := store.UpdateOperator(ctx, ownerID, ada.ID, storage.UpdateOperatorParams{QueueIDs: &none}); err != nil {
		t.Fatalf("unassign Ada: %v", err)
	}
	seats, _ = store.Seats(ctx, q.ID)
	if seats[1].Worker != nil {
		t.Fatalf("chair 2 after Ada left the queue = %+v, want nobody", seats[1].Worker)
	}
}
