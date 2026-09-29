-- +goose Up

-- How numbers are given out. SEQUENTIAL is the queue as it has always been:
-- 1, 2, 3 in the order people join. RANDOM hands each joiner a random unused
-- number from 1 to the queue's places, and the queue is still called from the
-- lowest number up: the running order is set by chance, not by who scanned
-- first. Independent of serving_order, which says who is called next.
ALTER TABLE queues ADD COLUMN numbering text NOT NULL DEFAULT 'SEQUENTIAL'
    CHECK (numbering IN ('SEQUENTIAL', 'RANDOM'));

-- Random numbers are drawn from 1 to the number of places, so there must be
-- a number of places.
ALTER TABLE queues ADD CONSTRAINT random_numbers_require_capacity
    CHECK (numbering <> 'RANDOM' OR max_capacity IS NOT NULL);

-- One kind of chance at a time: random numbers called in order, or ordinary
-- numbers called at random. Both at once would be a draw over a draw.
ALTER TABLE queues ADD CONSTRAINT one_kind_of_random
    CHECK (NOT (numbering = 'RANDOM' AND serving_order = 'RANDOM'));

-- +goose Down

ALTER TABLE queues DROP CONSTRAINT one_kind_of_random;
ALTER TABLE queues DROP CONSTRAINT random_numbers_require_capacity;
ALTER TABLE queues DROP COLUMN numbering;
