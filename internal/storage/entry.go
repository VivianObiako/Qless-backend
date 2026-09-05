package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vivianobiako/qless/api/internal/queue"
)

const entryColumns = `id, queue_id, number, customer_name, status, joined_at, started_at, completed_at, presence, presence_at, walk_in`

func scanEntry(row pgx.Row) (queue.Entry, error) {
	var e queue.Entry
	var status string
	var presence *string
	err := row.Scan(
		&e.ID, &e.QueueID, &e.Number, &e.CustomerName, &status,
		&e.JoinedAt, &e.StartedAt, &e.CompletedAt,
		&presence, &e.PresenceAt, &e.WalkIn,
	)
	if err != nil {
		return queue.Entry{}, err
	}
	e.Status = queue.EntryStatus(status)
	if presence != nil {
		p := queue.Presence(*presence)
		e.Presence = &p
	}
	return e, nil
}

// SetPresence records what the customer has said about where they are, on
// their active entry. A customer with no active entry has nothing to say it
// about, which is the same answer Leave gives.
func (s *Store) SetPresence(ctx context.Context, queueID, customerTokenHash string, presence queue.Presence) (queue.Entry, error) {
	entry, err := scanEntry(s.pool.QueryRow(ctx,
		`UPDATE queue_entries SET presence = $3::entry_presence, presence_at = now()
		 WHERE queue_id = $1 AND customer_token_hash = $2 AND status IN ('WAITING', 'SERVING')
		 RETURNING `+entryColumns,
		queueID, customerTokenHash, string(presence),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Entry{}, queue.ErrNotInQueue
	}
	if err != nil {
		return queue.Entry{}, fmt.Errorf("set presence: %w", err)
	}
	return entry, nil
}

// Join places a customer in the queue and hands back their number.
//
// The whole thing runs under a row lock on the queue: that lock is what makes
// number assignment safe. Two customers tapping "Join" at the same instant
// serialise here, so they cannot receive the same number.
//
// A customer who already holds an active entry gets that entry back alongside
// ErrAlreadyJoined rather than a duplicate place in line.
func (s *Store) Join(ctx context.Context, queueID, customerName, customerTokenHash string) (queue.Entry, error) {
	return s.join(ctx, queueID, customerName, customerTokenHash, false)
}

// AddWalkIn places somebody in the queue from the counter. The token hash is
// for a token nobody holds, so the entry is a number and a name and nothing a
// phone can recover; the flag lets the counter say as much.
func (s *Store) AddWalkIn(ctx context.Context, queueID, customerName, discardedTokenHash string) (queue.Entry, error) {
	return s.join(ctx, queueID, customerName, discardedTokenHash, true)
}

func (s *Store) join(ctx context.Context, queueID, customerName, customerTokenHash string, walkIn bool) (queue.Entry, error) {
	var entry queue.Entry

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var status string
		var maxCapacity *int
		var nextNumber int

		err := tx.QueryRow(ctx,
			`SELECT status, max_capacity, next_number FROM queues WHERE id = $1 FOR UPDATE`, queueID,
		).Scan(&status, &maxCapacity, &nextNumber)
		if errors.Is(err, pgx.ErrNoRows) {
			return queue.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock queue: %w", err)
		}

		switch queue.Status(status) {
		case queue.StatusPaused:
			return queue.ErrQueuePaused
		case queue.StatusClosed:
			return queue.ErrQueueClosed
		}

		existing, err := scanEntry(tx.QueryRow(ctx,
			`SELECT `+entryColumns+` FROM queue_entries
			 WHERE queue_id = $1 AND customer_token_hash = $2 AND status IN ('WAITING', 'SERVING')`,
			queueID, customerTokenHash,
		))
		if err == nil {
			entry = existing
			return queue.ErrAlreadyJoined
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check existing entry: %w", err)
		}

		if maxCapacity != nil {
			var active int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM queue_entries WHERE queue_id = $1 AND status IN ('WAITING', 'SERVING')`, queueID,
			).Scan(&active); err != nil {
				return fmt.Errorf("count active entries: %w", err)
			}
			if active >= *maxCapacity {
				return queue.ErrQueueFull
			}
		}

		entry, err = scanEntry(tx.QueryRow(ctx,
			`INSERT INTO queue_entries (queue_id, number, customer_name, customer_token_hash, walk_in)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING `+entryColumns,
			queueID, nextNumber, customerName, customerTokenHash, walkIn,
		))
		if err != nil {
			return fmt.Errorf("insert entry: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`UPDATE queues SET next_number = next_number + 1, updated_at = now() WHERE id = $1`, queueID,
		); err != nil {
			return fmt.Errorf("advance next number: %w", err)
		}

		return nil
	})

	// ErrAlreadyJoined is a meaningful answer, not a failure: the caller wants
	// the existing entry so the customer sees their position again.
	if errors.Is(err, queue.ErrAlreadyJoined) {
		return entry, queue.ErrAlreadyJoined
	}
	if err != nil {
		return queue.Entry{}, err
	}
	return entry, nil
}

// MyEntry returns the customer's most recent entry in this queue, whatever its
// status. Returning ended entries too is deliberate: it is how a skipped
// customer is told they were skipped instead of silently seeing a join form.
func (s *Store) MyEntry(ctx context.Context, queueID, customerTokenHash string) (queue.Entry, error) {
	entry, err := scanEntry(s.pool.QueryRow(ctx,
		`SELECT `+entryColumns+` FROM queue_entries
		 WHERE queue_id = $1 AND customer_token_hash = $2
		 ORDER BY joined_at DESC, number DESC
		 LIMIT 1`,
		queueID, customerTokenHash,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Entry{}, queue.ErrNotInQueue
	}
	if err != nil {
		return queue.Entry{}, fmt.Errorf("get my entry: %w", err)
	}
	return entry, nil
}

// Leave marks the customer's active entry as LEFT. Nothing is deleted; the
// record stays in history and the freed slot reopens capacity.
func (s *Store) Leave(ctx context.Context, queueID, customerTokenHash string) (queue.Entry, error) {
	entry, err := scanEntry(s.pool.QueryRow(ctx,
		`UPDATE queue_entries SET status = 'LEFT', completed_at = now()
		 WHERE queue_id = $1 AND customer_token_hash = $2 AND status IN ('WAITING', 'SERVING')
		 RETURNING `+entryColumns,
		queueID, customerTokenHash,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Entry{}, queue.ErrNotInQueue
	}
	if err != nil {
		return queue.Entry{}, fmt.Errorf("leave queue: %w", err)
	}
	return entry, nil
}

// ServeResult reports what a serve operation changed, so the caller can tell
// the customer who was just attended apart from the one now being served.
type ServeResult struct {
	Served   *queue.Entry `json:"served"`
	Attended *queue.Entry `json:"attended"`
}

// ServeNext attends whoever is currently being served and promotes the lowest
// waiting number in their place. Both halves happen under the queue row lock,
// so two operators clicking at once cannot serve the same customer twice.
func (s *Store) ServeNext(ctx context.Context, queueID string, actor queue.Actor) (ServeResult, error) {
	var result ServeResult

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockQueue(ctx, tx, queueID); err != nil {
			return err
		}

		attended, err := attendCurrent(ctx, tx, queueID, actor)
		if err != nil {
			return err
		}
		result.Attended = attended

		actorType, operatorID := actedBy(actor)
		served, err := scanEntry(tx.QueryRow(ctx,
			`UPDATE queue_entries SET status = 'SERVING', started_at = now(),
			        acted_by_type = $2::principal_type, acted_by_operator_id = $3
			 WHERE id = (
				 SELECT id FROM queue_entries
				 WHERE queue_id = $1 AND status = 'WAITING'
				 ORDER BY number
				 LIMIT 1
			 )
			 RETURNING `+entryColumns,
			queueID, actorType, operatorID,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			// Queue is empty. Attending the previous customer still stands.
			return nil
		}
		if err != nil {
			return fmt.Errorf("serve next: %w", err)
		}
		result.Served = &served
		return nil
	})
	if err != nil {
		return ServeResult{}, err
	}
	return result, nil
}

// ServeEntry calls one specific customer to the counter.
//
// Whoever was at the counter is attended first, exactly as ServeNext does —
// there is one counter, and the database enforces it with a unique index. What
// this does *not* do is reorder anyone: the customer is lifted out of the line
// and everybody else keeps the position they had.
func (s *Store) ServeEntry(ctx context.Context, queueID, entryID string, actor queue.Actor) (ServeResult, error) {
	var result ServeResult

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockQueue(ctx, tx, queueID); err != nil {
			return err
		}

		target, err := lockEntry(ctx, tx, queueID, entryID)
		if err != nil {
			return err
		}

		// Already at the counter. Answering with the entry rather than an error
		// keeps a double click on a slow connection harmless.
		if target.Status == queue.EntryServing {
			result.Served = &target
			return nil
		}

		// A skipped customer can be called back with the number they had,
		// for a while. After that the record is history, not a place in line.
		switch target.Status {
		case queue.EntryWaiting:
		case queue.EntrySkipped:
			if target.CompletedAt == nil || time.Since(*target.CompletedAt) > queue.RecallWindow {
				return queue.ErrRecallExpired
			}
		default:
			return queue.ErrEntryNotActive
		}

		attended, err := attendCurrent(ctx, tx, queueID, actor)
		if err != nil {
			return err
		}
		result.Attended = attended

		actorType, operatorID := actedBy(actor)
		served, err := scanEntry(tx.QueryRow(ctx,
			`UPDATE queue_entries SET status = 'SERVING', started_at = now(), completed_at = NULL,
			        acted_by_type = $2::principal_type, acted_by_operator_id = $3
			 WHERE id = $1
			 RETURNING `+entryColumns,
			entryID, actorType, operatorID,
		))
		if isUniqueViolation(err, "one_active_entry_per_number") {
			// The queue was reset since they were skipped and their number
			// has been handed out again. Nothing to recall them to.
			return queue.ErrRecallExpired
		}
		if err != nil {
			return fmt.Errorf("serve entry: %w", err)
		}
		result.Served = &served
		return nil
	})
	if err != nil {
		return ServeResult{}, err
	}
	return result, nil
}

// AttendEntry closes out one customer as dealt with.
//
// It accepts a waiting entry as well as the one at the counter: an operator who
// serves someone without formally calling them first should be able to say so,
// rather than having to call them in order to finish with them.
func (s *Store) AttendEntry(ctx context.Context, queueID, entryID string, actor queue.Actor) (queue.Entry, error) {
	return s.endEntry(ctx, queueID, entryID, queue.EntryAttended, actor)
}

// SkipEntry stands a customer down without attending them. The record stays in
// history and the entry falls outside the active-entry index, which is what
// lets that customer rejoin and take a fresh number.
func (s *Store) SkipEntry(ctx context.Context, queueID, entryID string, actor queue.Actor) (queue.Entry, error) {
	return s.endEntry(ctx, queueID, entryID, queue.EntrySkipped, actor)
}

func (s *Store) endEntry(
	ctx context.Context,
	queueID, entryID string,
	status queue.EntryStatus,
	actor queue.Actor,
) (queue.Entry, error) {
	var entry queue.Entry

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockQueue(ctx, tx, queueID); err != nil {
			return err
		}

		target, err := lockEntry(ctx, tx, queueID, entryID)
		if err != nil {
			return err
		}
		if !target.Status.IsActive() {
			return queue.ErrEntryNotActive
		}

		actorType, operatorID := actedBy(actor)
		entry, err = scanEntry(tx.QueryRow(ctx,
			`UPDATE queue_entries SET status = $2, completed_at = now(),
			        acted_by_type = $3::principal_type, acted_by_operator_id = $4
			 WHERE id = $1
			 RETURNING `+entryColumns,
			entryID, string(status), actorType, operatorID,
		))
		if err != nil {
			return fmt.Errorf("end entry: %w", err)
		}
		return nil
	})
	if err != nil {
		return queue.Entry{}, err
	}
	return entry, nil
}

// ResetQueue clears the line and starts the numbering again.
//
// Nothing is deleted: every active entry becomes CLEARED and stays in history,
// so the day that just ended is still there to read. There is no automatic
// date-based reset anywhere in Qless — a queue starts over when an operator
// says it does.
func (s *Store) ResetQueue(ctx context.Context, queueID string) (int, error) {
	var cleared int

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockQueue(ctx, tx, queueID); err != nil {
			return err
		}

		tag, err := tx.Exec(ctx,
			`UPDATE queue_entries SET status = 'CLEARED', completed_at = now()
			 WHERE queue_id = $1 AND status IN ('WAITING', 'SERVING')`,
			queueID,
		)
		if err != nil {
			return fmt.Errorf("clear entries: %w", err)
		}
		cleared = int(tag.RowsAffected())

		if _, err := tx.Exec(ctx,
			`UPDATE queues SET next_number = 1, updated_at = now() WHERE id = $1`, queueID,
		); err != nil {
			return fmt.Errorf("reset next number: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return cleared, nil
}

// History returns the entries this queue has finished with, most recent first,
// each carrying whoever dealt with it.
//
// The operator's name is joined rather than copied onto the entry, so renaming
// a member of staff corrects the whole of their history rather than leaving it
// stamped with a name they no longer use.
func (s *Store) History(ctx context.Context, queueID string, limit int) ([]queue.HistoryEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+prefixed(entryColumns, "e")+`, e.acted_by_type, o.display_name
		   FROM queue_entries e
		   LEFT JOIN operators o ON o.id = e.acted_by_operator_id
		  WHERE e.queue_id = $1 AND e.status NOT IN ('WAITING', 'SERVING')
		  ORDER BY e.completed_at DESC NULLS LAST, e.number DESC
		  LIMIT $2`,
		queueID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	defer rows.Close()

	entries := []queue.HistoryEntry{}
	for rows.Next() {
		var (
			entry        queue.Entry
			status       string
			presence     *string
			actedByType  *string
			operatorName *string
		)
		err := rows.Scan(
			&entry.ID, &entry.QueueID, &entry.Number, &entry.CustomerName, &status,
			&entry.JoinedAt, &entry.StartedAt, &entry.CompletedAt,
			&presence, &entry.PresenceAt, &entry.WalkIn,
			&actedByType, &operatorName,
		)
		if err != nil {
			return nil, fmt.Errorf("scan history entry: %w", err)
		}
		entry.Status = queue.EntryStatus(status)
		if presence != nil {
			p := queue.Presence(*presence)
			entry.Presence = &p
		}

		row := queue.HistoryEntry{Entry: entry}
		// Absent on entries the customer ended themselves, and on everything
		// that happened before this column existed.
		if actedByType != nil {
			acted := queue.ActedBy{Type: queue.PrincipalType(*actedByType)}
			if operatorName != nil {
				acted.OperatorName = *operatorName
			}
			row.ActedBy = &acted
		}
		entries = append(entries, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history: %w", err)
	}
	return entries, nil
}

// actingOperator is the operator id to stamp on an entry, or nil for an owner.
// An owner needs no id: the queue already says which business this is, and
// there is exactly one owner behind it.
func actingOperator(actor queue.Actor) *string {
	if actor.Type != queue.PrincipalOperator {
		return nil
	}
	return &actor.ID
}

// lockEntry reads one entry for update, scoped to its queue so an id from
// another business resolves to nothing.
func lockEntry(ctx context.Context, tx pgx.Tx, queueID, entryID string) (queue.Entry, error) {
	entry, err := scanEntry(tx.QueryRow(ctx,
		`SELECT `+entryColumns+` FROM queue_entries WHERE id = $1 AND queue_id = $2 FOR UPDATE`,
		entryID, queueID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Entry{}, queue.ErrEntryNotFound
	}
	if err != nil {
		return queue.Entry{}, fmt.Errorf("lock entry: %w", err)
	}
	return entry, nil
}

func lockQueue(ctx context.Context, tx pgx.Tx, queueID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM queues WHERE id = $1 FOR UPDATE`, queueID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock queue: %w", err)
	}
	return nil
}

// attendCurrent closes out the entry currently being served, if there is one.
// actedBy splits an actor into the two columns the acted_by_matches_type
// constraint expects: an owner carries no operator id, an operator always does.
func actedBy(actor queue.Actor) (string, any) {
	if actor.Type == queue.PrincipalOperator {
		return string(queue.PrincipalOperator), actor.ID
	}
	return string(queue.PrincipalOwner), nil
}

func attendCurrent(ctx context.Context, tx pgx.Tx, queueID string, actor queue.Actor) (*queue.Entry, error) {
	actorType, operatorID := actedBy(actor)
	attended, err := scanEntry(tx.QueryRow(ctx,
		`UPDATE queue_entries SET status = 'ATTENDED', completed_at = now(),
		        acted_by_type = $2::principal_type, acted_by_operator_id = $3
		 WHERE queue_id = $1 AND status = 'SERVING'
		 RETURNING `+entryColumns,
		queueID, actorType, operatorID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("attend current: %w", err)
	}
	return &attended, nil
}

// ListRecentlySkipped returns the customers stood down inside the recall
// window, most recent first: the ones the counter can still call back.
func (s *Store) ListRecentlySkipped(ctx context.Context, queueID string) ([]queue.Entry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+entryColumns+` FROM queue_entries
		 WHERE queue_id = $1 AND status = 'SKIPPED' AND completed_at > now() - $2::interval
		 ORDER BY completed_at DESC`,
		queueID, queue.RecallWindow,
	)
	if err != nil {
		return nil, fmt.Errorf("list recently skipped: %w", err)
	}
	defer rows.Close()

	entries := []queue.Entry{}
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan skipped entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skipped entries: %w", err)
	}
	return entries, nil
}

// ListActiveEntries returns the customer being served plus everyone waiting,
// in queue order. Operator-only: this is the one place names are returned.
func (s *Store) ListActiveEntries(ctx context.Context, queueID string) ([]queue.Entry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+entryColumns+` FROM queue_entries
		 WHERE queue_id = $1 AND status IN ('SERVING', 'WAITING')
		 ORDER BY (status = 'SERVING') DESC, number`,
		queueID,
	)
	if err != nil {
		return nil, fmt.Errorf("list active entries: %w", err)
	}
	defer rows.Close()

	entries := []queue.Entry{}
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate entries: %w", err)
	}
	return entries, nil
}
