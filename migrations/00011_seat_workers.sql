-- +goose Up

-- Who works a seat. An operator is assigned to a chair by the owner on the
-- roster, or picks a free one on the counter; the owner can take a chair
-- too, which is what lets a one-person shop run its own counter. Two
-- columns because the owner has no operator row: either the operator id is
-- set, or the owner flag is, or the chair is nobody's.
--
-- SET NULL rather than RESTRICT so that removing a business never trips over
-- its chairs; operators are soft-revoked in practice and revoke clears this.
ALTER TABLE seats ADD COLUMN worker_operator_id uuid REFERENCES operators (id) ON DELETE SET NULL;
ALTER TABLE seats ADD COLUMN worked_by_owner boolean NOT NULL DEFAULT false;

ALTER TABLE seats ADD CONSTRAINT seat_has_one_worker
    CHECK (NOT (worked_by_owner AND worker_operator_id IS NOT NULL));

-- An operator works one chair per queue. The owner is the same person on
-- every chair they hold, which the storage layer keeps to one as well.
CREATE UNIQUE INDEX one_seat_per_operator_per_queue
    ON seats (queue_id, worker_operator_id)
    WHERE worker_operator_id IS NOT NULL AND removed_at IS NULL;

-- Chairs are fixed: staff work the chair the owner gave them and cannot pick
-- another. Off by default, so a shop where anybody sits anywhere never has
-- to know the setting exists.
ALTER TABLE queues ADD COLUMN seats_fixed boolean NOT NULL DEFAULT false;

-- +goose Down

ALTER TABLE queues DROP COLUMN seats_fixed;
DROP INDEX IF EXISTS one_seat_per_operator_per_queue;
ALTER TABLE seats DROP CONSTRAINT seat_has_one_worker;
ALTER TABLE seats DROP COLUMN worked_by_owner;
ALTER TABLE seats DROP COLUMN worker_operator_id;
