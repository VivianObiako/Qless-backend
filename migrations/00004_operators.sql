-- +goose Up

-- Operators are the second principal: named people who work the counter without
-- holding the business. They exist under exactly one owner and reach only the
-- queues they are assigned to.
CREATE TYPE operator_status AS ENUM ('ACTIVE', 'REVOKED');

CREATE TABLE operators (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id         uuid NOT NULL REFERENCES owners (id) ON DELETE CASCADE,
    display_name     text NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 60),
    -- Cleared on revoke rather than left to be checked at redeem: a code that
    -- is not in the table cannot be redeemed by any code path anyone adds
    -- later, and it frees the value for reuse.
    access_code_hash text UNIQUE,
    status           operator_status NOT NULL DEFAULT 'ACTIVE',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_operators_owner ON operators (owner_id);

CREATE TABLE operator_queues (
    operator_id uuid NOT NULL REFERENCES operators (id) ON DELETE CASCADE,
    queue_id    uuid NOT NULL REFERENCES queues (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (operator_id, queue_id)
);

CREATE INDEX idx_operator_queues_queue ON operator_queues (queue_id);

-- The other half of the principal check 00002 left open. An access token now
-- points at exactly one principal, and its type says which column to read.
ALTER TABLE access_tokens ADD COLUMN operator_id uuid REFERENCES operators (id) ON DELETE CASCADE;

ALTER TABLE access_tokens DROP CONSTRAINT owner_token_has_owner;

ALTER TABLE access_tokens ADD CONSTRAINT principal_matches_type CHECK (
    (principal_type = 'OWNER'    AND owner_id IS NOT NULL AND operator_id IS NULL) OR
    (principal_type = 'OPERATOR' AND operator_id IS NOT NULL AND owner_id IS NULL)
);

CREATE INDEX idx_access_tokens_operator ON access_tokens (operator_id);

-- Who acted on an entry.
--
-- This column has to exist from the moment a second principal can act, because
-- history written without it can never be attributed afterwards — there is no
-- record anywhere else of who pressed the button. Rows written before now are
-- unambiguous anyway: the owner was the only principal that could exist.
--
-- Two columns rather than three: the queue already says which business this is,
-- so an owner action needs no id, and ON DELETE SET NULL is a formality —
-- revoking an operator is a soft delete precisely so this keeps resolving to a
-- name long after they have gone.
ALTER TABLE queue_entries ADD COLUMN acted_by_type principal_type;
ALTER TABLE queue_entries ADD COLUMN acted_by_operator_id uuid REFERENCES operators (id) ON DELETE SET NULL;

ALTER TABLE queue_entries ADD CONSTRAINT acted_by_matches_type CHECK (
    (acted_by_type IS NULL AND acted_by_operator_id IS NULL) OR
    (acted_by_type = 'OWNER' AND acted_by_operator_id IS NULL) OR
    (acted_by_type = 'OPERATOR' AND acted_by_operator_id IS NOT NULL)
);

-- +goose Down

ALTER TABLE queue_entries DROP CONSTRAINT acted_by_matches_type;
ALTER TABLE queue_entries DROP COLUMN acted_by_operator_id;
ALTER TABLE queue_entries DROP COLUMN acted_by_type;

DROP INDEX IF EXISTS idx_access_tokens_operator;

-- Operator sessions cannot survive in a schema with nowhere to point them.
DELETE FROM access_tokens WHERE principal_type = 'OPERATOR';

ALTER TABLE access_tokens DROP CONSTRAINT principal_matches_type;
ALTER TABLE access_tokens ADD CONSTRAINT owner_token_has_owner CHECK (principal_type <> 'OWNER' OR owner_id IS NOT NULL);
ALTER TABLE access_tokens DROP COLUMN operator_id;

DROP TABLE IF EXISTS operator_queues;
DROP TABLE IF EXISTS operators;
DROP TYPE IF EXISTS operator_status;
