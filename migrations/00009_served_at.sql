-- +goose Up

-- When service actually began, as distinct from when the number was called.
-- The gap between the two is the customer walking back; the gap from here to
-- completed_at is the service itself, which is what an estimate should be
-- built from. Set by inference (called while already here, said "here" at
-- the counter, recalled from a skip) or by one tap on the counter; null for
-- an entry nobody marked, which then falls back to the call.
ALTER TABLE queue_entries ADD COLUMN served_at timestamptz;

-- +goose Down

ALTER TABLE queue_entries DROP COLUMN served_at;
