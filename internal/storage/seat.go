package storage

import (
	"context"
	"errors"
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

// CreateSeat adds a seat at the end of the queue's order, open.
func (s *Store) CreateSeat(ctx context.Context, queueID, name string) (queue.Seat, error) {
	seat, err := scanSeat(s.pool.QueryRow(ctx,
		`INSERT INTO seats (queue_id, name, position)
		 VALUES ($1, $2, (SELECT COALESCE(MAX(position), 0) + 1 FROM seats WHERE queue_id = $1))
		 RETURNING `+seatColumns,
		queueID, name,
	))
	if err != nil {
		return queue.Seat{}, fmt.Errorf("insert seat: %w", err)
	}
	return seat, nil
}

// pickSeat settles which seat a call lands on, inside the transaction that
// is about to make it.
//
// A named seat must be this queue's and open. No seat named means the lowest
// free open seat; if none is free and the queue has exactly one open seat,
// that seat is reused and its occupant stood down, which is what every
// client from before seats expects. With several seats all taken, the caller
// has to say whose person to stand down, so that is an error.
func pickSeat(ctx context.Context, tx pgx.Tx, queueID, seatID string) (string, error) {
	if seatID != "" {
		var active bool
		err := tx.QueryRow(ctx,
			`SELECT active FROM seats WHERE id = $1 AND queue_id = $2 AND removed_at IS NULL`,
			seatID, queueID,
		).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", queue.ErrSeatNotFound
		}
		if err != nil {
			return "", fmt.Errorf("read seat: %w", err)
		}
		if !active {
			return "", queue.ErrSeatClosed
		}
		return seatID, nil
	}

	rows, err := tx.Query(ctx,
		`SELECT s.id, NOT EXISTS (
		            SELECT 1 FROM queue_entries e WHERE e.seat_id = s.id AND e.status = 'SERVING'
		        ) AS free
		   FROM seats s
		  WHERE s.queue_id = $1 AND s.removed_at IS NULL AND s.active
		  ORDER BY s.position, s.created_at`,
		queueID,
	)
	if err != nil {
		return "", fmt.Errorf("list open seats: %w", err)
	}
	defer rows.Close()

	var open []string
	for rows.Next() {
		var id string
		var free bool
		if err := rows.Scan(&id, &free); err != nil {
			return "", fmt.Errorf("scan open seat: %w", err)
		}
		if free {
			return id, nil
		}
		open = append(open, id)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate open seats: %w", err)
	}

	if len(open) == 1 {
		return open[0], nil
	}
	return "", queue.ErrNoFreeSeat
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
