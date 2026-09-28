-- +goose Up

-- How the counter picks who is called next. IN_ORDER is the queue as it has
-- always been: the lowest waiting number. RANDOM is a draw: everyone holds a
-- number and the counter calls one at random, with one drawn ahead as "up
-- next" so the wall never goes blank. Text with a check rather than a
-- boolean, so a third way of choosing does not need a second migration.
ALTER TABLE queues ADD COLUMN serving_order text NOT NULL DEFAULT 'IN_ORDER'
    CHECK (serving_order IN ('IN_ORDER', 'RANDOM'));

-- A draw has a fixed number of places. The API refuses the combination too;
-- this closes the gap between two settings requests racing each other.
ALTER TABLE queues ADD CONSTRAINT random_requires_capacity
    CHECK (serving_order <> 'RANDOM' OR max_capacity IS NOT NULL);

-- What the people in the queue are called, on their phones and on the wall:
-- customer and customers, guest and guests, participant and participants.
-- No case is forced, so "VIP" survives.
ALTER TABLE queues ADD COLUMN person_noun text NOT NULL DEFAULT 'customer'
    CHECK (length(btrim(person_noun)) BETWEEN 1 AND 30);
ALTER TABLE queues ADD COLUMN people_noun text NOT NULL DEFAULT 'customers'
    CHECK (length(btrim(people_noun)) BETWEEN 1 AND 30);

-- When the numbering last started again. Null for a queue never reset. In a
-- draw the places are counted from here, so yesterday's numbers do not fill
-- today's event.
ALTER TABLE queues ADD COLUMN reset_at timestamptz;

-- The one waiting entry drawn as up next. Set by the draw and kept after the
-- call, so history can say when a number was drawn as well as when it was
-- called.
ALTER TABLE queue_entries ADD COLUMN drawn_at timestamptz;

-- One drawn number per queue while it waits. Two operators pressing at once
-- fail here rather than on a check the application hoped to remember.
CREATE UNIQUE INDEX one_drawn_per_queue
    ON queue_entries (queue_id)
    WHERE status = 'WAITING' AND drawn_at IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS one_drawn_per_queue;
ALTER TABLE queue_entries DROP COLUMN drawn_at;
ALTER TABLE queues DROP COLUMN reset_at;
ALTER TABLE queues DROP COLUMN people_noun;
ALTER TABLE queues DROP COLUMN person_noun;
ALTER TABLE queues DROP CONSTRAINT random_requires_capacity;
ALTER TABLE queues DROP COLUMN serving_order;
