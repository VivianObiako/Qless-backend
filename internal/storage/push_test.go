package storage_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vivianobiako/qless/api/internal/push"
	"github.com/vivianobiako/qless/api/internal/token"
)

// Every queue event starts its own nudge pass, so two passes can look at the
// same phone at once. Only one of them may tell it about a rung, or the phone
// hears "you're next" twice.
func TestOnlyOnePassClaimsARung(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	join(t, store, q.ID, "subscriber")
	if err := store.SavePushSubscription(ctx, q.ID, token.Hash("subscriber"+q.ID), push.Subscription{
		Endpoint: "https://push.example/" + freshSecret(t), P256dh: "key", Auth: "auth",
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	targets, err := store.PushTargets(ctx, q.ID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("push targets: %v, %d", err, len(targets))
	}
	id := targets[0].ID

	const passes = 10
	var won atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range passes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := store.ClaimPushRung(ctx, id, 2)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if ok {
				won.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if won.Load() != 1 {
		t.Fatalf("%d passes claimed the same rung, want exactly 1", won.Load())
	}
	if ok, _ := store.ClaimPushRung(ctx, id, 2); ok {
		t.Fatal("a rung already claimed was claimed again")
	}
	if ok, _ := store.ClaimPushRung(ctx, id, 1); ok {
		t.Fatal("a lower rung was claimed after a higher one")
	}

	// A send that failed hands the rung back, and the next frame can try.
	if err := store.ReleasePushRung(ctx, id, 2, 0); err != nil {
		t.Fatalf("release: %v", err)
	}
	if ok, _ := store.ClaimPushRung(ctx, id, 2); !ok {
		t.Fatal("a released rung could not be claimed again")
	}

	// A release only undoes its own claim: once a higher rung is claimed, an
	// older pass giving back rung 2 changes nothing.
	if ok, _ := store.ClaimPushRung(ctx, id, 3); !ok {
		t.Fatal("could not claim the next rung")
	}
	if err := store.ReleasePushRung(ctx, id, 2, 0); err != nil {
		t.Fatalf("stale release: %v", err)
	}
	if ok, _ := store.ClaimPushRung(ctx, id, 3); ok {
		t.Fatal("a stale release undid a later claim")
	}
}

// A push address is kept only while its visit is live: once the customer
// leaves or is served, the next pass over the queue removes it.
func TestFinishedVisitsLoseTheirPushAddress(t *testing.T) {
	store := newTestStore(t)
	q := newTestQueue(t, store, nil)
	ctx := context.Background()

	for _, name := range []string{"leaver", "stayer"} {
		join(t, store, q.ID, name)
		if err := store.SavePushSubscription(ctx, q.ID, token.Hash(name+q.ID), push.Subscription{
			Endpoint: "https://push.example/" + freshSecret(t), P256dh: "key", Auth: "auth",
		}); err != nil {
			t.Fatalf("subscribe %s: %v", name, err)
		}
	}
	if _, err := store.Leave(ctx, q.ID, token.Hash("leaver"+q.ID)); err != nil {
		t.Fatalf("leave: %v", err)
	}

	if err := store.ForgetFinishedPushSubscriptions(ctx, q.ID); err != nil {
		t.Fatalf("forget: %v", err)
	}

	var left int
	if err := store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM push_subscriptions p JOIN queue_entries e ON e.id = p.entry_id WHERE e.queue_id = $1`,
		q.ID,
	).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 1 {
		t.Fatalf("%d push addresses left, want only the one still waiting", left)
	}
}
