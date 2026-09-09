package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vivianobiako/qless/api/internal/queue"
)

// seatColumns joins the worker's name in, so a tile can say who is at the
// chair without a second query: the operator's display name, or the owner's.
const seatColumns = `s.id, s.queue_id, s.name, s.position, s.active, s.removed_at, s.created_at, s.updated_at,
	s.worker_operator_id, s.worked_by_owner, COALESCE(o.display_name, ''), COALESCE(w.display_name, '')`

const seatJoins = `FROM seats s
	LEFT JOIN operators o ON o.id = s.worker_operator_id
	JOIN queues q ON q.id = s.queue_id
	JOIN owners w ON w.id = q.owner_id`

func scanSeat(row pgx.Row) (queue.Seat, error) {
	var seat queue.Seat
	var operatorID *string
	var byOwner bool
	var operatorName, ownerName string
	err := row.Scan(
		&seat.ID, &seat.QueueID, &seat.Name, &seat.Position, &seat.Active,
		&seat.RemovedAt, &seat.CreatedAt, &seat.UpdatedAt,
		&operatorID, &byOwner, &operatorName, &ownerName,
	)
	if err != nil {
		return queue.Seat{}, err
	}
	switch {
	case operatorID != nil:
		seat.Worker = &queue.SeatWorker{Type: queue.PrincipalOperator, OperatorID: *operatorID, Name: operatorName}
	case byOwner:
		seat.Worker = &queue.SeatWorker{Type: queue.PrincipalOwner, Name: ownerName}
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

// querier is the part of a pool and a transaction the seat reads need, so
// the same list can be read inside a transaction that just changed it.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Seats lists a queue's seats in position order, removed ones left out.
func (s *Store) Seats(ctx context.Context, queueID string) ([]queue.Seat, error) {
	return listSeats(ctx, s.pool, queueID)
}

func listSeats(ctx context.Context, db querier, queueID string) ([]queue.Seat, error) {
	rows, err := db.Query(ctx,
		`SELECT `+seatColumns+` `+seatJoins+`
		 WHERE s.queue_id = $1 AND s.removed_at IS NULL
		 ORDER BY s.position, s.created_at`,
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

// lockSeat reads one live seat for update, scoped to its queue so an id
// from another business resolves to nothing.
func lockSeat(ctx context.Context, tx pgx.Tx, queueID, seatID string) (queue.Seat, error) {
	// FOR UPDATE cannot sit on the outer side of a join, so the lock is
	// taken on the row alone and the joined read follows it.
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id FROM seats WHERE id = $1 AND queue_id = $2 AND removed_at IS NULL FOR UPDATE`,
		seatID, queueID,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Seat{}, queue.ErrSeatNotFound
	}
	if err != nil {
		return queue.Seat{}, fmt.Errorf("lock seat: %w", err)
	}
	seat, err := scanSeat(tx.QueryRow(ctx, `SELECT `+seatColumns+` `+seatJoins+` WHERE s.id = $1`, seatID))
	if err != nil {
		return queue.Seat{}, fmt.Errorf("read seat: %w", err)
	}
	return seat, nil
}

// seatOccupied reports whether somebody is being served at the seat.
func seatOccupied(ctx context.Context, tx pgx.Tx, seatID string) (bool, error) {
	var occupied bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM queue_entries WHERE seat_id = $1 AND status = 'SERVING')`, seatID,
	).Scan(&occupied); err != nil {
		return false, fmt.Errorf("check seat occupancy: %w", err)
	}
	return occupied, nil
}

// seatNameFree refuses a second live seat with the same name in one queue:
// two chairs called "Chair 2" would send two customers to the same place.
func seatNameFree(ctx context.Context, tx pgx.Tx, queueID, name, exceptID string) error {
	var taken bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM seats
		     WHERE queue_id = $1 AND removed_at IS NULL AND lower(name) = lower($2) AND id <> $3
		 )`,
		queueID, name, exceptID,
	).Scan(&taken); err != nil {
		return fmt.Errorf("check seat name: %w", err)
	}
	if taken {
		return fmt.Errorf("%w: There is already a chair called %s.", queue.ErrInvalidInput, name)
	}
	return nil
}

// renumberSeats closes the gaps after a move or a removal so positions are
// always 1..n in order.
func renumberSeats(ctx context.Context, tx pgx.Tx, queueID string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE seats s SET position = ranked.position
		   FROM (SELECT id, row_number() OVER (ORDER BY position, created_at) AS position
		           FROM seats WHERE queue_id = $1 AND removed_at IS NULL) ranked
		  WHERE s.id = ranked.id AND s.position <> ranked.position`,
		queueID,
	); err != nil {
		return fmt.Errorf("renumber seats: %w", err)
	}
	return nil
}

// CreateSeat adds a seat at the end of the queue's order, open.
func (s *Store) CreateSeat(ctx context.Context, queueID, name string) (queue.Seat, error) {
	var seat queue.Seat
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockQueue(ctx, tx, queueID); err != nil {
			return err
		}
		if err := seatNameFree(ctx, tx, queueID, name, "00000000-0000-0000-0000-000000000000"); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx,
			`INSERT INTO seats (queue_id, name, position)
			 VALUES ($1, $2, (SELECT COALESCE(MAX(position), 0) + 1 FROM seats WHERE queue_id = $1 AND removed_at IS NULL))
			 RETURNING id`,
			queueID, name,
		).Scan(&id); err != nil {
			return fmt.Errorf("insert seat: %w", err)
		}
		created, err := lockSeat(ctx, tx, queueID, id)
		if err != nil {
			return err
		}
		seat = created
		return nil
	})
	if err != nil {
		return queue.Seat{}, err
	}
	return seat, nil
}

// UpdateSeatParams is a partial update: a nil field is one the owner did
// not mention. Position is 1-based and moves the seat, shifting the rest.
type UpdateSeatParams struct {
	Name     *string
	Active   *bool
	Position *int
}

// UpdateSeat renames, reorders, opens or closes a seat. Closing a seat
// somebody is being served at is refused: finish with them first.
func (s *Store) UpdateSeat(ctx context.Context, queueID, seatID string, p UpdateSeatParams) ([]queue.Seat, error) {
	return s.seatsAfter(ctx, queueID, func(tx pgx.Tx) error {
		seat, err := lockSeat(ctx, tx, queueID, seatID)
		if err != nil {
			return err
		}

		if p.Name != nil {
			if err := seatNameFree(ctx, tx, queueID, *p.Name, seatID); err != nil {
				return err
			}
		}
		if p.Active != nil && !*p.Active && seat.Active {
			occupied, err := seatOccupied(ctx, tx, seatID)
			if err != nil {
				return err
			}
			if occupied {
				return queue.ErrSeatOccupied
			}
		}

		if _, err := tx.Exec(ctx,
			`UPDATE seats SET name = COALESCE($2, name), active = COALESCE($3, active), updated_at = now()
			 WHERE id = $1`,
			seatID, p.Name, p.Active,
		); err != nil {
			return fmt.Errorf("update seat: %w", err)
		}

		if p.Position != nil {
			// Move by giving the seat a half-step position and renumbering:
			// above the target for a move up, below it for a move down.
			target := float64(*p.Position)
			if *p.Position > seat.Position {
				target += 0.5
			} else {
				target -= 0.5
			}
			if _, err := tx.Exec(ctx,
				`UPDATE seats SET position = (SELECT COALESCE(MAX(position), 0) FROM seats WHERE queue_id = $2) + 1 WHERE id = $1`,
				seatID, queueID,
			); err != nil {
				return fmt.Errorf("move seat: %w", err)
			}
			if _, err := tx.Exec(ctx,
				`UPDATE seats s SET position = ranked.position
				   FROM (SELECT id, row_number() OVER (ORDER BY CASE WHEN id = $2 THEN $3::float ELSE position::float END, created_at) AS position
				           FROM seats WHERE queue_id = $1 AND removed_at IS NULL) ranked
				  WHERE s.id = ranked.id`,
				queueID, seatID, target,
			); err != nil {
				return fmt.Errorf("reorder seats: %w", err)
			}
		}
		return nil
	})
}

// RemoveSeat takes a seat away. Soft: history rows point at it, so the row
// stays with removed_at set and its name keeps resolving. Refused while
// somebody is on it, and for a queue's last seat.
func (s *Store) RemoveSeat(ctx context.Context, queueID, seatID string) ([]queue.Seat, error) {
	return s.seatsAfter(ctx, queueID, func(tx pgx.Tx) error {
		if _, err := lockSeat(ctx, tx, queueID, seatID); err != nil {
			return err
		}
		occupied, err := seatOccupied(ctx, tx, seatID)
		if err != nil {
			return err
		}
		if occupied {
			return queue.ErrSeatOccupied
		}

		var live int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM seats WHERE queue_id = $1 AND removed_at IS NULL`, queueID,
		).Scan(&live); err != nil {
			return fmt.Errorf("count seats: %w", err)
		}
		if live <= 1 {
			return queue.ErrLastSeat
		}

		if _, err := tx.Exec(ctx,
			`UPDATE seats SET removed_at = now(), active = false,
			        worker_operator_id = NULL, worked_by_owner = false, updated_at = now()
			 WHERE id = $1`,
			seatID,
		); err != nil {
			return fmt.Errorf("remove seat: %w", err)
		}
		return nil
	})
}

// AssignSeat is the owner putting somebody at a chair from the roster or
// the counter: an operator, themselves, or nobody. An operator holds one
// chair per queue and so does the owner, so any other chair of theirs in
// this queue is given up first. The operator must be assigned to the queue.
func (s *Store) AssignSeat(ctx context.Context, queueID, seatID string, worker *queue.SeatWorker) ([]queue.Seat, error) {
	return s.seatsAfter(ctx, queueID, func(tx pgx.Tx) error {
		if _, err := lockSeat(ctx, tx, queueID, seatID); err != nil {
			return err
		}
		return setWorker(ctx, tx, queueID, seatID, worker)
	})
}

func setWorker(ctx context.Context, tx pgx.Tx, queueID, seatID string, worker *queue.SeatWorker) error {
	var operatorID *string
	byOwner := false
	if worker != nil {
		switch worker.Type {
		case queue.PrincipalOperator:
			var assigned bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (
				    SELECT 1 FROM operator_queues oq
				      JOIN operators o ON o.id = oq.operator_id
				     WHERE oq.operator_id = $1 AND oq.queue_id = $2 AND o.status = 'ACTIVE'
				 )`,
				worker.OperatorID, queueID,
			).Scan(&assigned); err != nil {
				return fmt.Errorf("check operator assignment: %w", err)
			}
			if !assigned {
				return queue.ErrOperatorNotFound
			}
			id := worker.OperatorID
			operatorID = &id
			if _, err := tx.Exec(ctx,
				`UPDATE seats SET worker_operator_id = NULL, updated_at = now()
				 WHERE queue_id = $1 AND worker_operator_id = $2 AND id <> $3`,
				queueID, id, seatID,
			); err != nil {
				return fmt.Errorf("give up other seat: %w", err)
			}
		case queue.PrincipalOwner:
			byOwner = true
			if _, err := tx.Exec(ctx,
				`UPDATE seats SET worked_by_owner = false, updated_at = now()
				 WHERE queue_id = $1 AND worked_by_owner AND id <> $2`,
				queueID, seatID,
			); err != nil {
				return fmt.Errorf("give up other seat: %w", err)
			}
		default:
			return fmt.Errorf("%w: Say who works this chair.", queue.ErrInvalidInput)
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE seats SET worker_operator_id = $2, worked_by_owner = $3, updated_at = now() WHERE id = $1`,
		seatID, operatorID, byOwner,
	); err != nil {
		return fmt.Errorf("set seat worker: %w", err)
	}
	return nil
}

// TakeSeat is the actor picking a chair for themselves on the counter.
//
// The owner may take any chair, including one somebody is working, who is
// moved off and told on their next frame. An operator may take only a free
// chair — nobody's, and nobody being served at it — and not at all on a
// queue where the owner has fixed the chairs.
func (s *Store) TakeSeat(ctx context.Context, queueID, seatID string, actor queue.Actor) ([]queue.Seat, error) {
	return s.seatsAfter(ctx, queueID, func(tx pgx.Tx) error {
		seat, err := lockSeat(ctx, tx, queueID, seatID)
		if err != nil {
			return err
		}
		if seat.WorkedBy(actor) {
			return nil
		}

		worker := &queue.SeatWorker{Type: queue.PrincipalOwner}
		if !actor.IsOwner() {
			var fixed bool
			if err := tx.QueryRow(ctx, `SELECT seats_fixed FROM queues WHERE id = $1`, queueID).Scan(&fixed); err != nil {
				return fmt.Errorf("read seats setting: %w", err)
			}
			if fixed {
				return queue.ErrSeatsFixed
			}
			if seat.Worker != nil {
				return queue.ErrSeatTaken
			}
			occupied, err := seatOccupied(ctx, tx, seatID)
			if err != nil {
				return err
			}
			if occupied {
				return queue.ErrSeatTaken
			}
			worker = &queue.SeatWorker{Type: queue.PrincipalOperator, OperatorID: actor.ID}
		}
		return setWorker(ctx, tx, queueID, seatID, worker)
	})
}

// LeaveSeat is the actor giving up the chair they hold. Nothing happens to
// whoever is being served there; the chair is simply nobody's. Refused for
// an operator on a queue with fixed chairs.
func (s *Store) LeaveSeat(ctx context.Context, queueID, seatID string, actor queue.Actor) ([]queue.Seat, error) {
	return s.seatsAfter(ctx, queueID, func(tx pgx.Tx) error {
		seat, err := lockSeat(ctx, tx, queueID, seatID)
		if err != nil {
			return err
		}
		if !seat.WorkedBy(actor) {
			return nil
		}
		if !actor.IsOwner() {
			var fixed bool
			if err := tx.QueryRow(ctx, `SELECT seats_fixed FROM queues WHERE id = $1`, queueID).Scan(&fixed); err != nil {
				return fmt.Errorf("read seats setting: %w", err)
			}
			if fixed {
				return queue.ErrSeatsFixed
			}
		}
		return setWorker(ctx, tx, queueID, seatID, nil)
	})
}

// seatsAfter runs a change under the queue lock and hands back the list as
// it stands afterwards, renumbered, so every seat change answers the same
// way and the screen replaces its list wholesale.
func (s *Store) seatsAfter(ctx context.Context, queueID string, change func(tx pgx.Tx) error) ([]queue.Seat, error) {
	var seats []queue.Seat
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockQueue(ctx, tx, queueID); err != nil {
			return err
		}
		if err := change(tx); err != nil {
			return err
		}
		if err := renumberSeats(ctx, tx, queueID); err != nil {
			return err
		}
		listed, err := listSeats(ctx, tx, queueID)
		if err != nil {
			return err
		}
		seats = listed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return seats, nil
}

// OperatorSeats lists the chairs each of an owner's operators holds, keyed
// by operator id, for the roster.
func (s *Store) OperatorSeats(ctx context.Context, ownerID string) (map[string][]queue.OperatorSeat, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT s.worker_operator_id, s.queue_id, s.id, s.name
		   FROM seats s
		   JOIN operators o ON o.id = s.worker_operator_id
		  WHERE o.owner_id = $1 AND s.removed_at IS NULL
		  ORDER BY s.position`,
		ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list operator seats: %w", err)
	}
	defer rows.Close()

	held := map[string][]queue.OperatorSeat{}
	for rows.Next() {
		var operatorID string
		var seat queue.OperatorSeat
		if err := rows.Scan(&operatorID, &seat.QueueID, &seat.SeatID, &seat.SeatName); err != nil {
			return nil, fmt.Errorf("scan operator seat: %w", err)
		}
		held[operatorID] = append(held[operatorID], seat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operator seats: %w", err)
	}
	return held, nil
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

// validSeatName trims and bounds a seat name the way the column does.
func ValidSeatName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("%w: Give the chair a name.", queue.ErrInvalidInput)
	}
	if len([]rune(name)) > 40 {
		return "", fmt.Errorf("%w: Chair names must be 40 characters or fewer.", queue.ErrInvalidInput)
	}
	return name, nil
}
