-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE queue_status AS ENUM ('OPEN', 'PAUSED', 'CLOSED');
CREATE TYPE entry_status AS ENUM ('WAITING', 'SERVING', 'ATTENDED', 'SKIPPED', 'LEFT', 'CLEARED');

CREATE TABLE queues (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 80),
    slug                    text NOT NULL UNIQUE,
    description             text NOT NULL DEFAULT '',
    average_service_minutes int NOT NULL CHECK (average_service_minutes BETWEEN 1 AND 480),
    max_capacity            int CHECK (max_capacity IS NULL OR max_capacity BETWEEN 1 AND 1000),
    status                  queue_status NOT NULL DEFAULT 'OPEN',
    next_number             int NOT NULL DEFAULT 1,
    owner_token_hash        text NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE queue_entries (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    queue_id            uuid NOT NULL REFERENCES queues (id) ON DELETE CASCADE,
    number              int NOT NULL,
    customer_name       text NOT NULL CHECK (length(btrim(customer_name)) BETWEEN 1 AND 60),
    customer_token_hash text NOT NULL,
    status              entry_status NOT NULL DEFAULT 'WAITING',
    joined_at           timestamptz NOT NULL DEFAULT now(),
    started_at          timestamptz,
    completed_at        timestamptz,
    UNIQUE (queue_id, number)
);

-- At most one customer may be SERVING per queue. Two operators racing to serve
-- cannot both win: the loser's transaction fails on this index, not on a check
-- we hope the application remembered to perform.
CREATE UNIQUE INDEX one_serving_per_queue
    ON queue_entries (queue_id)
    WHERE status = 'SERVING';

-- A customer token may hold at most one *active* entry per queue. Skipped and
-- departed entries fall outside the predicate, which is what lets those
-- customers rejoin and take a fresh number.
CREATE UNIQUE INDEX one_active_entry_per_token
    ON queue_entries (queue_id, customer_token_hash)
    WHERE status IN ('WAITING', 'SERVING');

CREATE INDEX idx_entries_queue_status ON queue_entries (queue_id, status);
CREATE INDEX idx_entries_queue_number ON queue_entries (queue_id, number);
CREATE INDEX idx_entries_token ON queue_entries (customer_token_hash);

-- +goose Down
DROP TABLE IF EXISTS queue_entries;
DROP TABLE IF EXISTS queues;
DROP TYPE IF EXISTS entry_status;
DROP TYPE IF EXISTS queue_status;
