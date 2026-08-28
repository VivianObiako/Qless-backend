package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vivianobiako/qless/api/internal/queue"
)

// touchInterval is how stale last_seen_at is allowed to get before a request
// pays for a write. Every operator action would otherwise carry an UPDATE, and
// the column exists to answer "is this tablet still in use", not to timestamp
// individual clicks.
const touchInterval = "5 minutes"

// ResolveActor maps a session token onto whoever is holding it, and marks the
// token as recently seen.
//
// The token hash is the primary key of the lookup rather than a value compared
// after fetching, so there is no secret to compare in variable time: either a
// row with that exact hash exists or the caller is nobody.
func (s *Store) ResolveActor(ctx context.Context, tokenHash string) (queue.Actor, error) {
	var (
		principal      string
		ownerID        *string
		operatorID     *string
		operatorOwner  *string
		operatorStatus *string
	)

	// One statement, one round trip: the update touches the row only when it has
	// gone stale, while the select answers regardless. The join is left, not
	// inner, so an owner's token does not need an operator to exist.
	err := s.pool.QueryRow(ctx,
		`WITH touched AS (
		     UPDATE access_tokens
		        SET last_seen_at = now()
		      WHERE token_hash = $1
		        AND (last_seen_at IS NULL OR last_seen_at < now() - interval '`+touchInterval+`')
		     RETURNING id
		 )
		 SELECT t.principal_type, t.owner_id, t.operator_id, o.owner_id, o.status
		   FROM access_tokens t
		   LEFT JOIN operators o ON o.id = t.operator_id
		  WHERE t.token_hash = $1`,
		tokenHash,
	).Scan(&principal, &ownerID, &operatorID, &operatorOwner, &operatorStatus)

	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Actor{}, queue.ErrUnauthorized
	}
	if err != nil {
		return queue.Actor{}, fmt.Errorf("resolve actor: %w", err)
	}

	switch queue.PrincipalType(principal) {
	case queue.PrincipalOwner:
		return queue.Actor{
			Type:    queue.PrincipalOwner,
			ID:      *ownerID,
			OwnerID: *ownerID,
		}, nil

	case queue.PrincipalOperator:
		// Revoking deletes an operator's tokens, so this should be unreachable.
		// It is checked anyway: a token that outlived a revoke by any route —
		// a race, a restored backup — must not still open a counter.
		if operatorID == nil || operatorOwner == nil ||
			queue.OperatorStatus(*operatorStatus) != queue.OperatorActive {
			return queue.Actor{}, queue.ErrUnauthorized
		}
		return queue.Actor{
			Type:    queue.PrincipalOperator,
			ID:      *operatorID,
			OwnerID: *operatorOwner,
		}, nil

	default:
		return queue.Actor{}, queue.ErrUnauthorized
	}
}

// CreateOwner mints a business and its first signed-in device together. Both
// hashes are supplied by the caller; this package never sees a raw code.
func (s *Store) CreateOwner(ctx context.Context, recoveryCodeHash, sessionTokenHash string) (string, error) {
	var ownerID string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO owners (recovery_code_hash) VALUES ($1) RETURNING id`, recoveryCodeHash,
		).Scan(&ownerID); err != nil {
			return fmt.Errorf("insert owner: %w", err)
		}
		return issueOwnerToken(ctx, tx, ownerID, sessionTokenHash)
	})
	if err != nil {
		return "", err
	}
	return ownerID, nil
}

// IssueOwnerToken records another signed-in device for an owner. Recovery adds
// a row here and removes none, which is the whole reason the table exists.
func (s *Store) IssueOwnerToken(ctx context.Context, ownerID, tokenHash string) error {
	return issueOwnerToken(ctx, s.pool, ownerID, tokenHash)
}

// execer is the overlap between a pool and a transaction, so the same insert
// serves a standalone call and one step of a larger unit of work.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func issueOwnerToken(ctx context.Context, db execer, ownerID, tokenHash string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO access_tokens (token_hash, principal_type, owner_id) VALUES ($1, 'OWNER', $2)`,
		tokenHash, ownerID,
	)
	if err != nil {
		return fmt.Errorf("issue owner token: %w", err)
	}
	return nil
}

// OwnerByRecoveryCode finds the business a code belongs to.
//
// A staged code that has not been acknowledged is accepted alongside the
// current one: whichever of the two the holder types, they have proved they
// hold it, and the caller settles which becomes current.
func (s *Store) OwnerByRecoveryCode(ctx context.Context, codeHash string) (string, error) {
	var ownerID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM owners WHERE recovery_code_hash = $1 OR pending_recovery_code_hash = $1`,
		codeHash,
	).Scan(&ownerID)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", queue.ErrInvalidCode
	}
	if err != nil {
		return "", fmt.Errorf("find owner by recovery code: %w", err)
	}
	return ownerID, nil
}

// RotateRecoveryCode settles the redeemed code as the current one and stages
// its replacement.
//
// Passing the redeemed hash in is what makes redeeming a staged code work: it
// becomes current, retiring whatever it replaced, and the new code waits behind
// it. Until AcknowledgeRecoveryCode runs, both are live — deliberately, because
// the alternative is a response lost in flight locking the owner out.
func (s *Store) RotateRecoveryCode(ctx context.Context, ownerID, redeemedHash, pendingHash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE owners
		    SET recovery_code_hash = $2,
		        pending_recovery_code_hash = $3,
		        updated_at = now()
		  WHERE id = $1`,
		ownerID, redeemedHash, pendingHash,
	)
	if err != nil {
		return fmt.Errorf("rotate recovery code: %w", err)
	}
	return nil
}

// AcknowledgeRecoveryCode promotes the staged code, which is the moment the
// redeemed one stops working. Acknowledging with nothing staged is not an
// error: the caller is asking for a state the row is already in.
func (s *Store) AcknowledgeRecoveryCode(ctx context.Context, ownerID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE owners
		    SET recovery_code_hash = pending_recovery_code_hash,
		        pending_recovery_code_hash = NULL,
		        updated_at = now()
		  WHERE id = $1 AND pending_recovery_code_hash IS NOT NULL`,
		ownerID,
	)
	if err != nil {
		return fmt.Errorf("acknowledge recovery code: %w", err)
	}
	return nil
}

// RevokeOtherOwnerSessions signs out the owner's other devices and returns how
// many it closed. Operator tokens are left alone: an owner replacing their own
// phone should not empty the counter tablet, and revoking staff is its own
// deliberate act on the roster screen.
func (s *Store) RevokeOtherOwnerSessions(ctx context.Context, ownerID, keepTokenHash string) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM access_tokens
		  WHERE owner_id = $1
		    AND principal_type = 'OWNER'
		    AND token_hash <> $2`,
		ownerID, keepTokenHash,
	)
	if err != nil {
		return 0, fmt.Errorf("revoke other sessions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// QueuesForActor lists everything this actor can open, oldest first. An
// operator with no assignments gets an empty list, which is a normal state
// rather than an error — the roster screen says "ask your manager".
func (s *Store) QueuesForActor(ctx context.Context, actor queue.Actor) ([]queue.Queue, error) {
	query := `SELECT ` + queueColumns + ` FROM queues WHERE owner_id = $1 ORDER BY created_at`
	if actor.Type == queue.PrincipalOperator {
		query = `SELECT ` + prefixed(queueColumns, "q") + `
		           FROM queues q
		           JOIN operator_queues oq ON oq.queue_id = q.id
		          WHERE oq.operator_id = $1
		          ORDER BY q.created_at`
	}

	rows, err := s.pool.Query(ctx, query, actor.ID)
	if err != nil {
		return nil, fmt.Errorf("list queues for actor: %w", err)
	}
	defer rows.Close()

	queues := []queue.Queue{}
	for rows.Next() {
		q, err := scanQueue(rows)
		if err != nil {
			return nil, fmt.Errorf("scan queue: %w", err)
		}
		queues = append(queues, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate queues for actor: %w", err)
	}
	return queues, nil
}

// AuthorizeQueue answers whether this actor may act on this queue at all.
//
// It asks per actor type rather than joining on owner_id, because owning the
// queue is one path to it and not the only one this schema will grow: an
// assigned operator arrives in 00003, and a service grouping would be a third.
// Callers get an answer, never a route.
func (s *Store) AuthorizeQueue(ctx context.Context, actor queue.Actor, queueID string) error {
	var query string

	switch actor.Type {
	case queue.PrincipalOwner:
		query = `SELECT EXISTS (SELECT 1 FROM queues WHERE id = $1 AND owner_id = $2)`
	case queue.PrincipalOperator:
		// Assignment is the operator's only path, and it is checked here rather
		// than trusted from the token: an owner who unassigns a queue expects
		// that to take effect on the next action, not on the next sign-in.
		query = `SELECT EXISTS (SELECT 1 FROM operator_queues WHERE queue_id = $1 AND operator_id = $2)`
	default:
		return queue.ErrUnauthorized
	}

	var permitted bool
	if err := s.pool.QueryRow(ctx, query, queueID, actor.ID).Scan(&permitted); err != nil {
		return fmt.Errorf("authorize queue: %w", err)
	}
	if !permitted {
		return queue.ErrUnauthorized
	}
	return nil
}
