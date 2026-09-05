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

const queueColumns = `id, name, slug, description, average_service_minutes, max_capacity, status, next_number, show_names_to_operators, hold_minutes, pause_note, archived_at, created_at, updated_at`

func scanQueue(row pgx.Row) (queue.Queue, error) {
	var q queue.Queue
	var status string
	err := row.Scan(
		&q.ID, &q.Name, &q.Slug, &q.Description,
		&q.AverageServiceMinutes, &q.MaxCapacity, &status, &q.NextNumber,
		&q.ShowNamesToOperators, &q.HoldMinutes, &q.PauseNote, &q.ArchivedAt, &q.CreatedAt, &q.UpdatedAt,
	)
	if err != nil {
		return queue.Queue{}, err
	}
	q.Status = queue.Status(status)
	return q, nil
}

type CreateQueueParams struct {
	Name                  string
	Description           string
	AverageServiceMinutes int
	MaxCapacity           *int

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
				`INSERT INTO queues (name, slug, description, average_service_minutes, max_capacity, owner_id)
				 VALUES ($1, $2, $3, $4, $5, $6)
				 RETURNING `+queueColumns,
				p.Name, slug, p.Description, p.AverageServiceMinutes, p.MaxCapacity, ownerID,
			))
			if err != nil {
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
}

// UpdateQueue applies the operator's settings. Changing the name does not
// change the slug: a queue's URL is printed on a sheet taped to a door, and
// renaming the business should not invalidate every code already in the wild.
func (s *Store) UpdateQueue(ctx context.Context, queueID string, p UpdateQueueParams) (queue.Queue, error) {
	q, err := scanQueue(s.pool.QueryRow(ctx,
		`UPDATE queues SET
		     name = COALESCE($2, name),
		     description = COALESCE($3, description),
		     average_service_minutes = COALESCE($4, average_service_minutes),
		     max_capacity = CASE WHEN $5 THEN $6 ELSE max_capacity END,
		     show_names_to_operators = COALESCE($7, show_names_to_operators),
		     hold_minutes = COALESCE($8, hold_minutes),
		     updated_at = now()
		 WHERE id = $1
		 RETURNING `+queueColumns,
		queueID, p.Name, p.Description, p.AverageServiceMinutes,
		p.MaxCapacitySet, p.MaxCapacity, p.ShowNamesToOperators, p.HoldMinutes,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Queue{}, queue.ErrNotFound
	}
	if err != nil {
		return queue.Queue{}, fmt.Errorf("update queue: %w", err)
	}
	return q, nil
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

// MeasuredService averages the last ten real start-to-finish times from the
// past twelve hours. Only entries that were called and then finished count:
// somebody marked as served straight from the list never had a service time.
func (s *Store) MeasuredService(ctx context.Context, queueID string) (queue.ServiceMeasure, error) {
	var (
		minutes float64
		sample  int
	)
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (completed_at - started_at)) / 60), 0), COUNT(*)
		   FROM (SELECT started_at, completed_at
		           FROM queue_entries
		          WHERE queue_id = $1 AND status = 'ATTENDED'
		            AND started_at IS NOT NULL AND completed_at > started_at
		            AND completed_at > now() - interval '12 hours'
		          ORDER BY completed_at DESC
		          LIMIT 10) recent`,
		queueID,
	).Scan(&minutes, &sample)
	if err != nil {
		return queue.ServiceMeasure{}, fmt.Errorf("measure service time: %w", err)
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
		WaitingNumbers: []int{},
	}

	measured, err := s.MeasuredService(ctx, q.ID)
	if err != nil {
		return queue.PublicState{}, err
	}
	state.ServiceMinutes = q.ServiceMinutesIn(measured)

	var servingNumber int
	err = s.pool.QueryRow(ctx,
		`SELECT number FROM queue_entries WHERE queue_id = $1 AND status = 'SERVING'`, q.ID,
	).Scan(&servingNumber)
	switch {
	case err == nil:
		state.ServingNumber = &servingNumber
	case errors.Is(err, pgx.ErrNoRows):
		// Nobody is being served yet; leave ServingNumber nil.
	default:
		return queue.PublicState{}, fmt.Errorf("read serving number: %w", err)
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
	state.Estimates = queue.EstimateTable(state.WaitingCount, state.ServiceMinutes)

	if q.MaxCapacity != nil {
		active := state.WaitingCount
		if state.ServingNumber != nil {
			active++
		}
		state.IsFull = active >= *q.MaxCapacity
	}

	return state, nil
}
