package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vivianobiako/qless/api/internal/queue"
)

const queueColumns = `id, name, slug, description, average_service_minutes, max_capacity, status, next_number, show_names_to_operators, hold_minutes, pause_note, seats_fixed, serving_order, person_noun, people_noun, reset_at, archived_at, created_at, updated_at`

func scanQueue(row pgx.Row) (queue.Queue, error) {
	var q queue.Queue
	var status, order string
	err := row.Scan(
		&q.ID, &q.Name, &q.Slug, &q.Description,
		&q.AverageServiceMinutes, &q.MaxCapacity, &status, &q.NextNumber,
		&q.ShowNamesToOperators, &q.HoldMinutes, &q.PauseNote, &q.SeatsFixed,
		&order, &q.PersonNoun, &q.PeopleNoun, &q.ResetAt,
		&q.ArchivedAt, &q.CreatedAt, &q.UpdatedAt,
	)
	if err != nil {
		return queue.Queue{}, err
	}
	q.Status = queue.Status(status)
	q.ServingOrder = queue.ServingOrder(order)
	return q, nil
}

type CreateQueueParams struct {
	Name                  string
	Description           string
	AverageServiceMinutes int
	MaxCapacity           *int
	ServingOrder          queue.ServingOrder
	PersonNoun            string
	PeopleNoun            string

	// OwnerID attaches the queue to a business that already exists. Leave it
	// empty to mint one, in which case the two hashes below are required: the
	// recovery code that gets this owner back in, and the session token issued
	// to the device creating the queue.
	OwnerID                  string
	NewOwnerRecoveryCodeHash string
	NewOwnerTokenHash        string
	NewOwnerName             string
}

// CreateQueueResult carries the owner alongside the queue, since creating a
// queue is also how a business comes into existence.
type CreateQueueResult struct {
	Queue   queue.Queue
	OwnerID string
}

// CreateQueue derives a slug from the business name, retrying with a random
// suffix when two businesses pick the same name.
//
// Owner, first token and queue are written in one transaction, and a slug
// collision retries the whole of it: a queues row is never left without an
// owner, and an abandoned attempt leaves no half-made business behind.
func (s *Store) CreateQueue(ctx context.Context, p CreateQueueParams) (CreateQueueResult, error) {
	// The API fills these in; the seed and the tests call the store
	// directly, and a queue made that way is an ordinary one.
	if p.ServingOrder == "" {
		p.ServingOrder = queue.ServingInOrder
	}
	if p.PersonNoun == "" {
		p.PersonNoun = queue.DefaultPersonNoun
	}
	if p.PeopleNoun == "" {
		p.PeopleNoun = queue.DefaultPeopleNoun
	}

	base := slugify(p.Name)

	for attempt := 0; attempt < 6; attempt++ {
		slug := base
		if attempt > 0 {
			slug = fmt.Sprintf("%s-%s", base, randomSuffix())
		}

		var result CreateQueueResult
		err := s.inTx(ctx, func(tx pgx.Tx) error {
			ownerID := p.OwnerID

			if ownerID == "" {
				if err := tx.QueryRow(ctx,
					`INSERT INTO owners (recovery_code_hash, display_name) VALUES ($1, $2) RETURNING id`,
					p.NewOwnerRecoveryCodeHash, p.NewOwnerName,
				).Scan(&ownerID); err != nil {
					return fmt.Errorf("insert owner: %w", err)
				}
				if err := issueOwnerToken(ctx, tx, ownerID, p.NewOwnerTokenHash); err != nil {
					return err
				}
			}

			q, err := scanQueue(tx.QueryRow(ctx,
				`INSERT INTO queues (name, slug, description, average_service_minutes, max_capacity, owner_id,
				                     serving_order, person_noun, people_noun)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				 RETURNING `+queueColumns,
				p.Name, slug, p.Description, p.AverageServiceMinutes, p.MaxCapacity, ownerID,
				string(p.ServingOrder), p.PersonNoun, p.PeopleNoun,
			))
			if isCheckViolation(err, "random_requires_capacity") {
				return errDrawNeedsPlaces
			}
			if err != nil {
				return err
			}
			if err := insertDefaultSeat(ctx, tx, q.ID); err != nil {
				return err
			}

			result = CreateQueueResult{Queue: q, OwnerID: ownerID}
			return nil
		})

		if err == nil {
			return result, nil
		}
		if isUniqueViolation(err, "queues_slug_key") {
			continue
		}
		return CreateQueueResult{}, fmt.Errorf("insert queue: %w", err)
	}

	return CreateQueueResult{}, errors.New("could not allocate a unique slug")
}

// GetQueue looks a queue up by id or by slug, whichever the key looks like.
func (s *Store) GetQueue(ctx context.Context, key string) (queue.Queue, error) {
	clause := "slug = $1"
	if IsUUID(key) {
		clause = "id = $1"
	}

	q, err := scanQueue(s.pool.QueryRow(ctx, `SELECT `+queueColumns+` FROM queues WHERE `+clause, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Queue{}, queue.ErrNotFound
	}
	if err != nil {
		return queue.Queue{}, fmt.Errorf("get queue: %w", err)
	}
	return q, nil
}

// UpdateQueueParams is a partial update: a nil field is one the caller did not
// mention. Capacity needs its own flag because clearing it — "no limit" — is a
// meaningful value that nil cannot express on its own.
type UpdateQueueParams struct {
	Name                  *string
	Description           *string
	AverageServiceMinutes *int
	MaxCapacitySet        bool
	MaxCapacity           *int
	ShowNamesToOperators  *bool
	HoldMinutes           *int
	SeatsFixed            *bool
	ServingOrder          *queue.ServingOrder
	PersonNoun            *string
	PeopleNoun            *string
}

// errDrawNeedsPlaces is the database refusing a draw with no capacity. The
// API refuses it first; this is what two settings requests racing get.
var errDrawNeedsPlaces = fmt.Errorf("%w: A draw needs a fixed number of places.", queue.ErrInvalidInput)

// UpdateQueue applies the operator's settings. Changing the name does not
// change the slug: a queue's URL is printed on a sheet taped to a door, and
// renaming the business should not invalidate every code already in the wild.
//
// It runs under the queue lock because a change of serving order touches the
// line: going back to serving in order drops the number a draw had picked,
// in the same transaction, so no frame ever shows both.
func (s *Store) UpdateQueue(ctx context.Context, queueID string, p UpdateQueueParams) (queue.Queue, error) {
	var order *string
	if p.ServingOrder != nil {
		value := string(*p.ServingOrder)
		order = &value
	}

	var q queue.Queue
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockQueue(ctx, tx, queueID); err != nil {
			return err
		}

		var err error
		q, err = scanQueue(tx.QueryRow(ctx,
			`UPDATE queues SET
			     name = COALESCE($2, name),
			     description = COALESCE($3, description),
			     average_service_minutes = COALESCE($4, average_service_minutes),
			     max_capacity = CASE WHEN $5 THEN $6 ELSE max_capacity END,
			     show_names_to_operators = COALESCE($7, show_names_to_operators),
			     hold_minutes = COALESCE($8, hold_minutes),
			     seats_fixed = COALESCE($9, seats_fixed),
			     serving_order = COALESCE($10, serving_order),
			     person_noun = COALESCE($11, person_noun),
			     people_noun = COALESCE($12, people_noun),
			     updated_at = now()
			 WHERE id = $1
			 RETURNING `+queueColumns,
			queueID, p.Name, p.Description, p.AverageServiceMinutes,
			p.MaxCapacitySet, p.MaxCapacity, p.ShowNamesToOperators, p.HoldMinutes, p.SeatsFixed,
			order, p.PersonNoun, p.PeopleNoun,
		))
		if isCheckViolation(err, "random_requires_capacity") {
			return errDrawNeedsPlaces
		}
		if err != nil {
			return fmt.Errorf("update queue: %w", err)
		}

		if !q.IsDraw() {
			if _, err := tx.Exec(ctx,
				`UPDATE queue_entries SET drawn_at = NULL
				 WHERE queue_id = $1 AND status = 'WAITING' AND drawn_at IS NOT NULL`,
				queueID,
			); err != nil {
				return fmt.Errorf("clear draw: %w", err)
			}
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Queue{}, queue.ErrNotFound
	}
	if err != nil {
		return queue.Queue{}, err
	}
	return q, nil
}

// PlacesTaken is placesTaken outside a transaction, for the counter.
func (s *Store) PlacesTaken(ctx context.Context, q queue.Queue) (int, error) {
	return placesTaken(ctx, s.pool, q)
}

// placesTaken is what a queue's capacity is measured against. A queue served
// in order counts the people in line, so a place frees up as each is served.
// A draw counts every number handed out since the last reset: a person who
// has presented does not free a place, a person who cancelled does.
func placesTaken(ctx context.Context, db querier, q queue.Queue) (int, error) {
	query := `SELECT count(*) FROM queue_entries WHERE queue_id = $1 AND status IN ('WAITING', 'SERVING')`
	args := []any{q.ID}
	if q.IsDraw() {
		query = `SELECT count(*) FROM queue_entries
		          WHERE queue_id = $1 AND status IN ('WAITING', 'SERVING', 'ATTENDED', 'SKIPPED')
		            AND ($2::timestamptz IS NULL OR joined_at >= $2)`
		args = append(args, q.ResetAt)
	}

	var taken int
	if err := db.QueryRow(ctx, query, args...).Scan(&taken); err != nil {
		return 0, fmt.Errorf("count places taken: %w", err)
	}
	return taken, nil
}

// SetStatus moves the queue between OPEN, PAUSED and CLOSED. Pausing and
// closing both stop new joins; neither disturbs the customers already in line.
// The note travels with a pause and is cleared by every other move, so a
// "back at 2:30" never outlives the break it described.
func (s *Store) SetStatus(ctx context.Context, queueID string, status queue.Status, note string) (queue.Queue, error) {
	if status != queue.StatusPaused {
		note = ""
	}
	q, err := scanQueue(s.pool.QueryRow(ctx,
		`UPDATE queues SET status = $2, pause_note = $3, updated_at = now() WHERE id = $1 RETURNING `+queueColumns,
		queueID, string(status), note,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Queue{}, queue.ErrNotFound
	}
	if err != nil {
		return queue.Queue{}, fmt.Errorf("set queue status: %w", err)
	}
	return q, nil
}

// SetArchived puts a queue away or brings it back. Archiving closes it as
// well, so nobody can join a queue its owner has stopped looking at; restoring
// leaves it closed, and reopening is the owner's next, separate decision.
func (s *Store) SetArchived(ctx context.Context, queueID string, archived bool) (queue.Queue, error) {
	query := `UPDATE queues SET archived_at = NULL, updated_at = now() WHERE id = $1 RETURNING ` + queueColumns
	if archived {
		query = `UPDATE queues SET archived_at = now(), status = 'CLOSED', pause_note = '', updated_at = now()
		          WHERE id = $1 RETURNING ` + queueColumns
	}
	q, err := scanQueue(s.pool.QueryRow(ctx, query, queueID))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Queue{}, queue.ErrNotFound
	}
	if err != nil {
		return queue.Queue{}, fmt.Errorf("set queue archived: %w", err)
	}
	return q, nil
}

// MeasuredService averages the last ten real service times from the past
// twelve hours: from when service began (or, for an entry nobody marked,
// from the call) to done. Only entries that were called and then finished
// count: somebody marked as served straight from the list never had one.
func (s *Store) MeasuredService(ctx context.Context, queueID string) (queue.ServiceMeasure, error) {
	return s.measure(ctx, queueID,
		`SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (completed_at - COALESCE(served_at, started_at))) / 60), 0), COUNT(*)
		   FROM (SELECT started_at, served_at, completed_at
		           FROM queue_entries
		          WHERE queue_id = $1 AND status = 'ATTENDED'
		            AND started_at IS NOT NULL AND completed_at > COALESCE(served_at, started_at)
		            AND completed_at > now() - interval '12 hours'
		          ORDER BY completed_at DESC
		          LIMIT 10) recent`)
}

// MeasuredServiceBySeat is MeasuredService per chair, for an owner comparing
// them: the last ten real service times at each chair in the past twelve
// hours. Chairs with nothing served lately come back with a zero sample.
func (s *Store) MeasuredServiceBySeat(ctx context.Context, queueID string) ([]queue.SeatMeasure, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT st.id, st.name,
		        COALESCE((SELECT AVG(EXTRACT(EPOCH FROM (completed_at - COALESCE(served_at, started_at))) / 60)
		                    FROM (SELECT started_at, served_at, completed_at
		                            FROM queue_entries
		                           WHERE seat_id = st.id AND status = 'ATTENDED'
		                             AND started_at IS NOT NULL AND completed_at > COALESCE(served_at, started_at)
		                             AND completed_at > now() - interval '12 hours'
		                           ORDER BY completed_at DESC
		                           LIMIT 10) recent), 0),
		        (SELECT count(*)
		           FROM (SELECT 1
		                   FROM queue_entries
		                  WHERE seat_id = st.id AND status = 'ATTENDED'
		                    AND started_at IS NOT NULL AND completed_at > COALESCE(served_at, started_at)
		                    AND completed_at > now() - interval '12 hours'
		                  ORDER BY completed_at DESC
		                  LIMIT 10) recent)
		   FROM seats st
		  WHERE st.queue_id = $1 AND st.removed_at IS NULL
		  ORDER BY st.position, st.created_at`,
		queueID,
	)
	if err != nil {
		return nil, fmt.Errorf("measure by seat: %w", err)
	}
	defer rows.Close()

	measures := []queue.SeatMeasure{}
	for rows.Next() {
		var m queue.SeatMeasure
		var minutes float64
		if err := rows.Scan(&m.SeatID, &m.SeatName, &minutes, &m.Sample); err != nil {
			return nil, fmt.Errorf("scan seat measure: %w", err)
		}
		m.Minutes = int(math.Round(minutes))
		if m.Sample > 0 && m.Minutes < 1 {
			m.Minutes = 1
		}
		measures = append(measures, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate seat measures: %w", err)
	}
	return measures, nil
}

// MeasuredArrival averages how long the last ten customers took to turn up
// after being called. Only entries where service was marked as begun count;
// it is the evidence for tuning the hold time.
func (s *Store) MeasuredArrival(ctx context.Context, queueID string) (queue.ServiceMeasure, error) {
	return s.measure(ctx, queueID,
		`SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (served_at - started_at)) / 60), 0), COUNT(*)
		   FROM (SELECT started_at, served_at
		           FROM queue_entries
		          WHERE queue_id = $1 AND status = 'ATTENDED'
		            AND started_at IS NOT NULL AND served_at IS NOT NULL AND served_at >= started_at
		            AND completed_at > now() - interval '12 hours'
		          ORDER BY completed_at DESC
		          LIMIT 10) recent`)
}

func (s *Store) measure(ctx context.Context, queueID, query string) (queue.ServiceMeasure, error) {
	var (
		minutes float64
		sample  int
	)
	if err := s.pool.QueryRow(ctx, query, queueID).Scan(&minutes, &sample); err != nil {
		return queue.ServiceMeasure{}, fmt.Errorf("measure: %w", err)
	}
	rounded := int(math.Round(minutes))
	if sample > 0 && rounded < 1 {
		rounded = 1
	}
	return queue.ServiceMeasure{Minutes: rounded, Sample: sample}, nil
}

// LastActivity is the moment anything last happened to this queue's entries,
// or nil for a queue nobody has ever joined. The dashboard reads it to ask
// whether a new day has started.
func (s *Store) LastActivity(ctx context.Context, queueID string) (*time.Time, error) {
	var at *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT GREATEST(MAX(joined_at), MAX(started_at), MAX(completed_at))
		   FROM queue_entries WHERE queue_id = $1`,
		queueID,
	).Scan(&at)
	if err != nil {
		return nil, fmt.Errorf("read last activity: %w", err)
	}
	return at, nil
}

// PublicState assembles the payload every customer and display screen sees.
// It reads numbers only — no names cross this boundary.
func (s *Store) PublicState(ctx context.Context, q queue.Queue) (queue.PublicState, error) {
	state := queue.PublicState{
		Queue:          q.Summary(),
		Serving:        []queue.ServingSlot{},
		Seats:          []queue.PublicSeat{},
		WaitingNumbers: []int{},
	}

	measured, err := s.MeasuredService(ctx, q.ID)
	if err != nil {
		return queue.PublicState{}, err
	}
	state.ServiceMinutes = q.ServiceMinutesIn(measured)

	seats, err := s.Seats(ctx, q.ID)
	if err != nil {
		return queue.PublicState{}, err
	}
	for _, seat := range seats {
		state.Seats = append(state.Seats, seat.Public())
		if seat.Active {
			state.OpenSeats++
		}
	}

	serving, err := s.pool.Query(ctx,
		`SELECT e.number, s.id, s.name, e.started_at
		   FROM queue_entries e
		   JOIN seats s ON s.id = e.seat_id
		  WHERE e.queue_id = $1 AND e.status = 'SERVING'
		  ORDER BY s.position, s.created_at`,
		q.ID,
	)
	if err != nil {
		return queue.PublicState{}, fmt.Errorf("read serving numbers: %w", err)
	}
	defer serving.Close()

	var latestCall *time.Time
	for serving.Next() {
		var slot queue.ServingSlot
		var startedAt *time.Time
		if err := serving.Scan(&slot.Number, &slot.SeatID, &slot.SeatName, &startedAt); err != nil {
			return queue.PublicState{}, fmt.Errorf("scan serving number: %w", err)
		}
		state.Serving = append(state.Serving, slot)

		// The most recent call is what a one-number board shows.
		if state.ServingNumber == nil || (startedAt != nil && (latestCall == nil || startedAt.After(*latestCall))) {
			number := slot.Number
			state.ServingNumber = &number
			latestCall = startedAt
		}
	}
	if err := serving.Err(); err != nil {
		return queue.PublicState{}, fmt.Errorf("iterate serving numbers: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT number FROM queue_entries WHERE queue_id = $1 AND status = 'WAITING' ORDER BY number`, q.ID,
	)
	if err != nil {
		return queue.PublicState{}, fmt.Errorf("read waiting numbers: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var number int
		if err := rows.Scan(&number); err != nil {
			return queue.PublicState{}, fmt.Errorf("scan waiting number: %w", err)
		}
		state.WaitingNumbers = append(state.WaitingNumbers, number)
	}
	if err := rows.Err(); err != nil {
		return queue.PublicState{}, fmt.Errorf("iterate waiting numbers: %w", err)
	}

	state.WaitingCount = len(state.WaitingNumbers)

	// A draw quotes no wait: nobody is ahead of anybody. The table is empty
	// rather than absent so a client indexing into it finds nothing, not an
	// error.
	state.Estimates = []*queue.Estimate{}
	if !q.IsDraw() {
		state.Estimates = queue.EstimateTable(state.WaitingCount, state.ServiceMinutes, state.OpenSeats)
	}

	if q.IsDraw() {
		var upNext int
		err := s.pool.QueryRow(ctx,
			`SELECT number FROM queue_entries WHERE queue_id = $1 AND status = 'WAITING' AND drawn_at IS NOT NULL`, q.ID,
		).Scan(&upNext)
		switch {
		case err == nil:
			state.UpNextNumber = &upNext
		case errors.Is(err, pgx.ErrNoRows):
			// Nothing drawn yet, or nobody left to draw.
		default:
			return queue.PublicState{}, fmt.Errorf("read up next: %w", err)
		}
	}

	taken, err := placesTaken(ctx, s.pool, q)
	if err != nil {
		return queue.PublicState{}, err
	}
	state.PlacesTaken = taken
	if q.MaxCapacity != nil {
		state.IsFull = taken >= *q.MaxCapacity
	}

	return state, nil
}
