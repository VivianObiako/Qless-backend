package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vivianobiako/qless/api/internal/queue"
)

// Every operator query is scoped by owner_id as well as by operator id. An
// owner holding a perfectly valid session must not be able to rename, reassign
// or revoke somebody else's staff by guessing an id.
const operatorColumns = `id, display_name, status, created_at, updated_at`

func scanOperator(row pgx.Row) (queue.Operator, error) {
	var o queue.Operator
	var status string
	if err := row.Scan(&o.ID, &o.DisplayName, &status, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return queue.Operator{}, err
	}
	o.Status = queue.OperatorStatus(status)
	o.QueueIDs = []string{}
	return o, nil
}

type CreateOperatorParams struct {
	OwnerID        string
	DisplayName    string
	AccessCodeHash string
	QueueIDs       []string
}

// CreateOperator adds a member of staff and assigns their queues in one
// transaction, so an operator never exists with an unfinished assignment.
func (s *Store) CreateOperator(ctx context.Context, p CreateOperatorParams) (queue.Operator, error) {
	var operator queue.Operator

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		created, err := scanOperator(tx.QueryRow(ctx,
			`INSERT INTO operators (owner_id, display_name, access_code_hash)
			 VALUES ($1, $2, $3)
			 RETURNING `+operatorColumns,
			p.OwnerID, p.DisplayName, p.AccessCodeHash,
		))
		if err != nil {
			return fmt.Errorf("insert operator: %w", err)
		}

		assigned, err := assignQueues(ctx, tx, p.OwnerID, created.ID, p.QueueIDs)
		if err != nil {
			return err
		}

		// What was actually assigned, not what was asked for. A queue id
		// belonging to another business is silently dropped by the insert, and
		// echoing the request back would report an assignment that does not
		// exist.
		created.QueueIDs = assigned
		operator = created
		return nil
	})
	if err != nil {
		return queue.Operator{}, err
	}
	return operator, nil
}

// assignQueues replaces an operator's assignments wholesale.
//
// The insert selects from queues filtered by owner, which is what stops an
// owner assigning their staff to a queue belonging to someone else: an id that
// is not theirs simply matches no row rather than raising an error that would
// tell them it exists.
func assignQueues(
	ctx context.Context,
	tx pgx.Tx,
	ownerID, operatorID string,
	queueIDs []string,
) ([]string, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM operator_queues WHERE operator_id = $1`, operatorID); err != nil {
		return nil, fmt.Errorf("clear queue assignments: %w", err)
	}

	assigned := []string{}
	if len(queueIDs) == 0 {
		return assigned, nil
	}

	rows, err := tx.Query(ctx,
		`INSERT INTO operator_queues (operator_id, queue_id)
		 SELECT $1, id FROM queues WHERE owner_id = $2 AND id = ANY($3)
		 RETURNING queue_id`,
		operatorID, ownerID, queueIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("assign queues: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var queueID string
		if err := rows.Scan(&queueID); err != nil {
			return nil, fmt.Errorf("scan assignment: %w", err)
		}
		assigned = append(assigned, queueID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assignments: %w", err)
	}
	return assigned, nil
}

// ListOperators returns the owner's whole roster, revoked staff included, with
// each one's assignments attached.
func (s *Store) ListOperators(ctx context.Context, ownerID string) ([]queue.Operator, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+operatorColumns+` FROM operators WHERE owner_id = $1 ORDER BY created_at`, ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list operators: %w", err)
	}
	defer rows.Close()

	operators := []queue.Operator{}
	index := map[string]int{}
	for rows.Next() {
		operator, err := scanOperator(rows)
		if err != nil {
			return nil, fmt.Errorf("scan operator: %w", err)
		}
		index[operator.ID] = len(operators)
		operators = append(operators, operator)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operators: %w", err)
	}
	if len(operators) == 0 {
		return operators, nil
	}

	assignments, err := s.pool.Query(ctx,
		`SELECT oq.operator_id, oq.queue_id
		   FROM operator_queues oq
		   JOIN operators o ON o.id = oq.operator_id
		  WHERE o.owner_id = $1`,
		ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list assignments: %w", err)
	}
	defer assignments.Close()

	for assignments.Next() {
		var operatorID, queueID string
		if err := assignments.Scan(&operatorID, &queueID); err != nil {
			return nil, fmt.Errorf("scan assignment: %w", err)
		}
		if at, ok := index[operatorID]; ok {
			operators[at].QueueIDs = append(operators[at].QueueIDs, queueID)
		}
	}
	if err := assignments.Err(); err != nil {
		return nil, fmt.Errorf("iterate assignments: %w", err)
	}
	return operators, nil
}

// GetOperator reads one operator, scoped to the owner asking for them.
func (s *Store) GetOperator(ctx context.Context, ownerID, operatorID string) (queue.Operator, error) {
	operator, err := scanOperator(s.pool.QueryRow(ctx,
		`SELECT `+operatorColumns+` FROM operators WHERE id = $1 AND owner_id = $2`,
		operatorID, ownerID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return queue.Operator{}, queue.ErrOperatorNotFound
	}
	if err != nil {
		return queue.Operator{}, fmt.Errorf("get operator: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT queue_id FROM operator_queues WHERE operator_id = $1`, operatorID,
	)
	if err != nil {
		return queue.Operator{}, fmt.Errorf("read assignments: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var queueID string
		if err := rows.Scan(&queueID); err != nil {
			return queue.Operator{}, fmt.Errorf("scan assignment: %w", err)
		}
		operator.QueueIDs = append(operator.QueueIDs, queueID)
	}
	if err := rows.Err(); err != nil {
		return queue.Operator{}, fmt.Errorf("iterate assignments: %w", err)
	}
	return operator, nil
}

// UpdateOperatorParams is a partial update: a nil field is one the owner did
// not mention. An empty (but non-nil) QueueIDs means "assigned to nothing",
// which is a real state the roster can express.
type UpdateOperatorParams struct {
	DisplayName *string
	QueueIDs    *[]string
}

func (s *Store) UpdateOperator(
	ctx context.Context,
	ownerID, operatorID string,
	p UpdateOperatorParams,
) (queue.Operator, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var found string
		err := tx.QueryRow(ctx,
			`UPDATE operators
			    SET display_name = COALESCE($3, display_name),
			        updated_at = now()
			  WHERE id = $1 AND owner_id = $2
			  RETURNING id`,
			operatorID, ownerID, p.DisplayName,
		).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return queue.ErrOperatorNotFound
		}
		if err != nil {
			return fmt.Errorf("update operator: %w", err)
		}

		if p.QueueIDs != nil {
			if _, err := assignQueues(ctx, tx, ownerID, operatorID, *p.QueueIDs); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return queue.Operator{}, err
	}
	return s.GetOperator(ctx, ownerID, operatorID)
}

// RegenerateOperatorCode issues a replacement code. The old one stops working
// immediately — unlike an owner's recovery code, this is handed over in person
// by somebody who can hand over another one.
func (s *Store) RegenerateOperatorCode(ctx context.Context, ownerID, operatorID, codeHash string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE operators
		    SET access_code_hash = $3, updated_at = now()
		  WHERE id = $1 AND owner_id = $2 AND status = 'ACTIVE'`,
		operatorID, ownerID, codeHash,
	)
	if err != nil {
		return fmt.Errorf("regenerate operator code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return queue.ErrOperatorNotFound
	}
	return nil
}

// RevokeOperator takes away everything at once: the code cannot be redeemed
// again, the assignments are gone, and every device they are signed in on stops
// working on its next request.
//
// The row itself stays. It is a soft delete so that entries this person handled
// keep resolving to a name in history — the point of recording who acted is
// lost if revoking erases them.
func (s *Store) RevokeOperator(ctx context.Context, ownerID, operatorID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE operators
			    SET status = 'REVOKED', access_code_hash = NULL, updated_at = now()
			  WHERE id = $1 AND owner_id = $2`,
			operatorID, ownerID,
		)
		if err != nil {
			return fmt.Errorf("revoke operator: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return queue.ErrOperatorNotFound
		}

		if _, err := tx.Exec(ctx,
			`DELETE FROM access_tokens WHERE operator_id = $1`, operatorID,
		); err != nil {
			return fmt.Errorf("revoke operator sessions: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`DELETE FROM operator_queues WHERE operator_id = $1`, operatorID,
		); err != nil {
			return fmt.Errorf("clear revoked assignments: %w", err)
		}
		return nil
	})
}

// OperatorByAccessCode finds the active operator holding a code. A revoked
// operator's code hash is NULL, so they cannot match here at all.
func (s *Store) OperatorByAccessCode(ctx context.Context, codeHash string) (string, error) {
	var operatorID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM operators WHERE access_code_hash = $1 AND status = 'ACTIVE'`, codeHash,
	).Scan(&operatorID)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", queue.ErrInvalidCode
	}
	if err != nil {
		return "", fmt.Errorf("find operator by access code: %w", err)
	}
	return operatorID, nil
}

// IssueOperatorToken records a signed-in device for an operator. Access codes
// are reusable, so redeeming one adds a session rather than replacing any.
func (s *Store) IssueOperatorToken(ctx context.Context, operatorID, tokenHash string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO access_tokens (token_hash, principal_type, operator_id) VALUES ($1, 'OPERATOR', $2)`,
		tokenHash, operatorID,
	)
	if err != nil {
		return fmt.Errorf("issue operator token: %w", err)
	}
	return nil
}
