-- +goose Up

-- Seats are rows, not a count. A seat is a place a customer is sent to — a
-- chair, a counter, an exam room — so it has a name the pass can print, can
-- be closed for the afternoon without being forgotten, and can carry an
-- operator. Position is the order the counter and the wall show them in.
--
-- A seat is never dropped: history rows point at it, so removing one sets
-- removed_at and the name keeps resolving. Removed seats fall out of every
-- list and out of the estimate's divisor; so do closed ones.
CREATE TABLE seats (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    queue_id   uuid NOT NULL REFERENCES queues (id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 40),
    position   int NOT NULL,
    active     boolean NOT NULL DEFAULT true,
    removed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_seats_queue_position ON seats (queue_id, position);

-- Which seat a customer was called to. Null while they wait; set by the
-- call and kept afterwards, so history can say which chair served them.
ALTER TABLE queue_entries ADD COLUMN seat_id uuid REFERENCES seats (id);

CREATE INDEX idx_entries_seat ON queue_entries (seat_id) WHERE seat_id IS NOT NULL;

-- Every queue that exists today has exactly one place to be served, so every
-- one of them gets exactly one seat, "Counter". A one-seat queue renders as
-- it always has; the difference begins when an owner adds a second.
INSERT INTO seats (queue_id, name, position)
SELECT id, 'Counter', 1 FROM queues;

-- Anybody who was ever called was called to that one counter. Pointing the
-- whole of history at it, not only whoever is being served now, means the
-- history screen's chair column has no blanks for the days before seats.
UPDATE queue_entries e
   SET seat_id = s.id
  FROM seats s
 WHERE s.queue_id = e.queue_id AND e.started_at IS NOT NULL;

-- The invariant moves one level down: one SERVING per seat rather than per
-- queue. A seat id is unique on its own, so the index needs no queue column.
-- A serving entry must name its seat, or the partial index has nothing to
-- hold two racing operators apart with.
DROP INDEX one_serving_per_queue;

CREATE UNIQUE INDEX one_serving_per_seat
    ON queue_entries (seat_id)
    WHERE status = 'SERVING';

ALTER TABLE queue_entries ADD CONSTRAINT serving_has_seat
    CHECK (status <> 'SERVING' OR seat_id IS NOT NULL);

-- +goose Down

-- Only reversible while no queue has two people being served at once:
-- restoring the per-queue index fails otherwise, which is the right outcome.
ALTER TABLE queue_entries DROP CONSTRAINT serving_has_seat;
DROP INDEX IF EXISTS one_serving_per_seat;

CREATE UNIQUE INDEX one_serving_per_queue
    ON queue_entries (queue_id)
    WHERE status = 'SERVING';

DROP INDEX IF EXISTS idx_entries_seat;
ALTER TABLE queue_entries DROP COLUMN seat_id;
DROP TABLE IF EXISTS seats;
