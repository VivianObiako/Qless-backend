-- +goose Up

-- Identity grows two layers. An owner is the business itself — there is still no
-- account and no person, only a recovery code that gets someone back in. An
-- access token is one signed-in device, and a principal may hold many of them,
-- which is what makes recovering on a new phone non-destructive: the new device
-- gets a row, and the counter tablet's row is never touched.
CREATE TYPE principal_type AS ENUM ('OWNER', 'OPERATOR');

CREATE TABLE owners (
    id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    recovery_code_hash         text NOT NULL UNIQUE,
    -- A rotated code is staged here rather than replacing the current one, and
    -- only becomes current once the holder acknowledges it. Rotating in place
    -- would mean a recovery response lost in flight — a dropped connection, a
    -- closed tab — leaves the owner holding a dead code and no way back in.
    pending_recovery_code_hash text UNIQUE,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE access_tokens (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash     text NOT NULL UNIQUE,
    principal_type principal_type NOT NULL,
    -- Operators arrive in 00003 with their own column and their own half of the
    -- check below. One polymorphic principal_id would have saved a column and
    -- cost every foreign key in this table.
    owner_id       uuid REFERENCES owners (id) ON DELETE CASCADE,
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz,
    CONSTRAINT owner_token_has_owner CHECK (principal_type <> 'OWNER' OR owner_id IS NOT NULL)
);

CREATE INDEX idx_access_tokens_owner ON access_tokens (owner_id);

ALTER TABLE queues ADD COLUMN owner_id uuid REFERENCES owners (id) ON DELETE CASCADE;

-- Owners always see customer names. Operators see them only where the queue
-- says so, which is why this defaults to off: a clinic should have to opt in.
ALTER TABLE queues ADD COLUMN show_names_to_operators boolean NOT NULL DEFAULT false;

-- Backfill. Every existing owner token is rehomed into access_tokens verbatim,
-- so every dashboard link and counter tablet already in the wild keeps working
-- across this migration.
--
-- One owner per distinct token, not per queue. Two queues sharing a token hash
-- were already operated by one holder — that token opens both dashboards — so
-- collapsing them into one business is what the old schema actually meant, and
-- it is the only reading that survives the UNIQUE constraint on token_hash.
--
-- The recovery code these owners get is random and is never printed anywhere.
-- That is deliberate: before this migration they had no recovery path at all,
-- so they are no worse off, and a code that only the database has cannot be
-- phished from them. Their route back in is the same as it was yesterday — the
-- bookmarked dashboard link.
-- +goose StatementBegin
DO $$
DECLARE
    existing   record;
    owner_uuid uuid;
BEGIN
    FOR existing IN SELECT DISTINCT owner_token_hash FROM queues LOOP
        INSERT INTO owners (recovery_code_hash)
        VALUES (encode(gen_random_bytes(32), 'hex'))
        RETURNING id INTO owner_uuid;

        INSERT INTO access_tokens (token_hash, principal_type, owner_id)
        VALUES (existing.owner_token_hash, 'OWNER', owner_uuid);

        UPDATE queues
           SET owner_id = owner_uuid
         WHERE owner_token_hash = existing.owner_token_hash;
    END LOOP;
END $$;
-- +goose StatementEnd

ALTER TABLE queues ALTER COLUMN owner_id SET NOT NULL;
ALTER TABLE queues DROP COLUMN owner_token_hash;

CREATE INDEX idx_queues_owner ON queues (owner_id);

-- +goose Down

-- Lossy by nature: an owner who signed in on three devices had three tokens,
-- and the old schema has room for one. The oldest wins, which is the token the
-- queue was created with. Queues whose owner has no tokens left get an
-- unguessable placeholder rather than a NULL the column cannot hold.
ALTER TABLE queues ADD COLUMN owner_token_hash text;

UPDATE queues q
   SET owner_token_hash = (
       SELECT t.token_hash
         FROM access_tokens t
        WHERE t.owner_id = q.owner_id
          AND t.principal_type = 'OWNER'
        ORDER BY t.created_at
        LIMIT 1
   );

UPDATE queues
   SET owner_token_hash = encode(gen_random_bytes(32), 'hex')
 WHERE owner_token_hash IS NULL;

ALTER TABLE queues ALTER COLUMN owner_token_hash SET NOT NULL;

DROP INDEX IF EXISTS idx_queues_owner;
ALTER TABLE queues DROP COLUMN show_names_to_operators;
ALTER TABLE queues DROP COLUMN owner_id;

DROP TABLE IF EXISTS access_tokens;
DROP TABLE IF EXISTS owners;
DROP TYPE IF EXISTS principal_type;

