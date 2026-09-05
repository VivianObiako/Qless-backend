-- +goose Up

-- How long a called customer's place is held, in minutes. It drives three
-- things at once so the promise reads the same on every surface: the counter
-- suggests a skip once a called person has been silent this long, a skipped
-- number stays recallable for this long, and the pass tells the customer the
-- figure. Zero means no hold: no nudge, and a skip is final.
ALTER TABLE queues ADD COLUMN hold_minutes integer NOT NULL DEFAULT 10
    CHECK (hold_minutes >= 0 AND hold_minutes <= 120);

-- A line shown on the pass and the wall while the queue is paused: "Back at
-- 2:30". Cleared on resume. Empty rather than null, since an absent note and a
-- blank one mean the same thing.
ALTER TABLE queues ADD COLUMN pause_note text NOT NULL DEFAULT '';

-- An archived queue is hidden from the owner's list and refuses joins, but
-- keeps every entry: history is the one thing this product never destroys.
-- Nullable because most queues are never archived.
ALTER TABLE queues ADD COLUMN archived_at timestamptz;

-- What the owner is called, on their own screens and in history. Optional;
-- an owner with no name is still "the owner".
ALTER TABLE owners ADD COLUMN display_name text NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE owners DROP COLUMN display_name;
ALTER TABLE queues DROP COLUMN archived_at;
ALTER TABLE queues DROP COLUMN pause_note;
ALTER TABLE queues DROP COLUMN hold_minutes;
