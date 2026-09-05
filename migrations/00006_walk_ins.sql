-- +goose Up

-- A person added at the counter by staff: someone without a phone, or a phone
-- that will not scan. They hold a number like anyone else. The token behind
-- their entry is minted and discarded, so no device can recover it, and this
-- flag is what lets the counter say so.
ALTER TABLE queue_entries ADD COLUMN walk_in boolean NOT NULL DEFAULT false;

-- +goose Down

ALTER TABLE queue_entries DROP COLUMN walk_in;
