-- +goose Up

-- A phone that asked to be told when it is close, next and up. One row per
-- browser endpoint, bound to the entry it was made for: a new number is a
-- new subscription, and an entry that ends takes its subscriptions with it.
-- last_rung is how far up the ladder this phone has been told, so a frame
-- that changes nothing for it sends nothing.
CREATE TABLE push_subscriptions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entry_id   uuid NOT NULL REFERENCES queue_entries(id) ON DELETE CASCADE,
    endpoint   text NOT NULL UNIQUE,
    p256dh     text NOT NULL,
    auth       text NOT NULL,
    last_rung  integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX push_subscriptions_entry_idx ON push_subscriptions (entry_id);

-- +goose Down

DROP TABLE push_subscriptions;
