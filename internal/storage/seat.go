package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vivianobiako/qless/api/internal/queue"
)

const seatColumns = `id, queue_id, name, position, active, removed_at, created_at, updated_at`

func scanSeat(row pgx.Row) (queue.Seat, error) {
	var seat queue.Seat
	err := row.Scan(
		&seat.ID, &seat.QueueID, &seat.Name, &seat.Position, &seat.Active,
		&seat.RemovedAt, &seat.CreatedAt, &seat.UpdatedAt,
	)
	if err != nil {
		return queue.Seat{}, err
	}
	return seat, nil
}

// DefaultSeatName is what the one seat a new queue starts with is called.
// It is the same name the migration gave every queue that existed before
// seats, so a one-seat shop never sees the word "seat" anywhere.
const DefaultSeatName = "Counter"

// insertDefaultSeat gives a freshly made queue its one seat, inside the
// transaction that made the queue, so a queue never exists with nowhere to
// serve anybody.
func insertDefaultSeat(ctx context.Context, tx pgx.Tx, queueID string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO seats (queue_id, name, position) VALUES ($1, $2, 1)`,
		queueID, DefaultSeatName,
	); err != nil {
		return fmt.Errorf("insert default seat: %w", err)
	}
	return nil
}

// Seats lists a queue's seats in position order, removed ones left out.
func (s *Store) Seats(ctx context.Context, queueID string) ([]queue.Seat, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+seatColumns+` FROM seats
		 WHERE queue_id = $1 AND removed_at IS NULL
		 ORDER BY position, created_at`,
		queueID,
	)
	if err != nil {
		return nil, fmt.Errorf("list seats: %w", err)
	}
	defer rows.Close()

	seats := []queue.Seat{}
	for rows.Next() {
		seat, err := scanSeat(rows)
		if err != nil {
			return nil, fmt.Errorf("scan seat: %w", err)
		}
		seats = append(seats, seat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate seats: %w", err)
	}
	return seats, nil
}
