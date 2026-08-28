package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vivianobiako/qless/api/internal/queue"
)

const queueColumns = `id, name, slug, description, average_service_minutes, max_capacity, status, next_number, show_names_to_operators, created_at, updated_at`

func scanQueue(row pgx.Row) (queue.Queue, error) {
	var q queue.Queue
	var status string
	err := row.Scan(
		&q.ID, &q.Name, &q.Slug, &q.Description,
		&q.AverageServiceMinutes, &q.MaxCapacity, &status, &q.NextNumber,
		&q.ShowNamesToOperators, &q.CreatedAt, &q.UpdatedAt,
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
					`INSERT INTO owners (recovery_code_hash) VALUES ($1) RETURNING id`,
					p.NewOwnerRecoveryCodeHash,
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
		     updated_at = now()
		 WHERE id = $1
		 RETURNING `+queueColumns,
		queueID, p.Name, p.Description, p.AverageServiceMinutes,
		p.MaxCapacitySet, p.MaxCapacity, p.ShowNamesToOperators,
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
func (s *Store) SetStatus(ctx context.Context, queueID string, status queue.Status) (queue.Queue, error) {
	q, err := scanQueue(s.pool.QueryRow(ctx,
		`UPDATE queues SET status = $2, updated_at = now() WHERE id = $1 RETURNING `+queueColumns,
		queueID, string(status),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Queue{}, queue.ErrNotFound
	}
	if err != nil {
		return queue.Queue{}, fmt.Errorf("set queue status: %w", err)
	}
	return q, nil
}

// PublicState assembles the payload every customer and display screen sees.
// It reads numbers only — no names cross this boundary.
func (s *Store) PublicState(ctx context.Context, q queue.Queue) (queue.PublicState, error) {
	state := queue.PublicState{
		Queue:          q.Summary(),
		WaitingNumbers: []int{},
	}

	var servingNumber int
	err := s.pool.QueryRow(ctx,
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
	state.Estimates = queue.EstimateTable(state.WaitingCount, q.AverageServiceMinutes)

	if q.MaxCapacity != nil {
		active := state.WaitingCount
		if state.ServingNumber != nil {
			active++
		}
		state.IsFull = active >= *q.MaxCapacity
	}

	return state, nil
}

