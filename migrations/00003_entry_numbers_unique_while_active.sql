-- +goose Up

-- Reset restarts numbering at 1 and keeps every past entry in history. With a
-- blanket UNIQUE (queue_id, number) those two are mutually exclusive: the first
-- customer of the new day collides with the cleared number 1 from the old one.
--
-- The invariant that actually matters is that two people *in the queue* cannot
-- hold the same number. Once an entry is attended, skipped, left or cleared it
-- is a record of something that happened, and its number is free again. That is
-- the same reasoning as one_active_entry_per_token, which is already partial for
-- exactly this reason.
ALTER TABLE queue_entries DROP CONSTRAINT queue_entries_queue_id_number_key;

CREATE UNIQUE INDEX one_active_entry_per_number
    ON queue_entries (queue_id, number)
    WHERE status IN ('WAITING', 'SERVING');

-- +goose Down

-- Only reversible on a database where no queue has ever been reset: restoring
-- the blanket constraint fails outright if any number has been reused, which is
-- the correct outcome — there is no safe way to invent numbers for those rows.
DROP INDEX IF EXISTS one_active_entry_per_number;

ALTER TABLE queue_entries ADD CONSTRAINT queue_entries_queue_id_number_key UNIQUE (queue_id, number);

