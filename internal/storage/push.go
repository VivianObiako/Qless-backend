package storage

import (
	"context"
	"fmt"

	"github.com/vivianobiako/qless/api/internal/push"
	"github.com/vivianobiako/qless/api/internal/queue"
)

// PushTarget is one phone that can still be told something: an active entry
// with a subscription, and how far up the ladder it has already been told.
type PushTarget struct {
	ID           string
	Number       int
	Status       queue.EntryStatus
	LastRung     int
	Subscription push.Subscription
}

// SavePushSubscription binds a browser's subscription to the caller's active
// entry. The endpoint is the identity: a phone that rejoins with a new number
// moves its subscription across and starts the ladder again.
func (s *Store) SavePushSubscription(ctx context.Context, queueID, customerTokenHash string, sub push.Subscription) error {
	entry, err := s.MyEntry(ctx, queueID, customerTokenHash)
	if err != nil {
		return err
	}
	if !entry.Status.IsActive() {
		return queue.ErrNotInQueue
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO push_subscriptions (entry_id, endpoint, p256dh, auth)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (endpoint) DO UPDATE
		    SET entry_id = EXCLUDED.entry_id,
		        p256dh = EXCLUDED.p256dh,
		        auth = EXCLUDED.auth,
		        last_rung = CASE WHEN push_subscriptions.entry_id = EXCLUDED.entry_id
		                         THEN push_subscriptions.last_rung ELSE 0 END`,
		entry.ID, sub.Endpoint, sub.P256dh, sub.Auth,
	)
	if err != nil {
		return fmt.Errorf("save push subscription: %w", err)
	}
	return nil
}

func (s *Store) DeletePushSubscription(ctx context.Context, endpoint string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint = $1`, endpoint); err != nil {
		return fmt.Errorf("delete push subscription: %w", err)
	}
	return nil
}

func (s *Store) DeletePushSubscriptionByID(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete push subscription: %w", err)
	}
	return nil
}

// PushTargets lists every subscribed phone still holding a place in this
// queue. Finished entries drop out here as well as by cascade, so a late
// frame never wakes a phone about a number that is over.
func (s *Store) PushTargets(ctx context.Context, queueID string) ([]PushTarget, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, e.number, e.status, p.last_rung, p.endpoint, p.p256dh, p.auth
		   FROM push_subscriptions p
		   JOIN queue_entries e ON e.id = p.entry_id
		  WHERE e.queue_id = $1 AND e.status IN ('WAITING', 'SERVING')`,
		queueID,
	)
	if err != nil {
		return nil, fmt.Errorf("list push targets: %w", err)
	}
	defer rows.Close()

	targets := []PushTarget{}
	for rows.Next() {
		var (
			t      PushTarget
			status string
		)
		if err := rows.Scan(&t.ID, &t.Number, &status, &t.LastRung,
			&t.Subscription.Endpoint, &t.Subscription.P256dh, &t.Subscription.Auth); err != nil {
			return nil, fmt.Errorf("scan push target: %w", err)
		}
		t.Status = queue.EntryStatus(status)
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate push targets: %w", err)
	}
	return targets, nil
}

func (s *Store) MarkPushRung(ctx context.Context, id string, rung int) error {
	if _, err := s.pool.Exec(ctx, `UPDATE push_subscriptions SET last_rung = $2 WHERE id = $1`, id, rung); err != nil {
		return fmt.Errorf("mark push rung: %w", err)
	}
	return nil
}
